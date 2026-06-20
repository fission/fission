// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/utils/correlation"
	"github.com/fission/fission/pkg/utils/httpx"
)

// defaultLocalFunctionName labels a local run when --name is omitted; it only
// shows up in the X-Fission-Function-Name header and failure messages.
const defaultLocalFunctionName = "local"

// specializeMaxRetries bounds the connect-refused backoff while the env
// container's HTTP server comes up — same budget as the in-cluster path.
const specializeMaxRetries = 30

// runConfig is the fully-resolved description of one local run (RFC-0018).
type runConfig struct {
	image        string
	envVersion   int
	entrypoint   string
	codePath     string
	functionMeta metav1.ObjectMeta
	method       string
	headers      []string
	body         string
	subPath      string
	keep         bool
}

type RunSubCommand struct {
	cmd.CommandActioner
}

// Run executes a function locally against its real env runtime image (RFC-0018):
// it runs the runtime in Docker, replays the /v2/specialize contract, and invokes
// it over the same path `function test` uses — no cluster round-trip.
func Run(input cli.Input) error {
	return (&RunSubCommand{}).do(input)
}

func (opts *RunSubCommand) do(input cli.Input) error {
	cfg, err := opts.resolveRunConfig(input)
	if err != nil {
		return err
	}
	rt, err := newDockerRuntime(logr.Logger{})
	if err != nil {
		return err
	}
	defer rt.Close()
	return runLocal(input.Context(), rt, cfg, os.Stdout, os.Stderr)
}

// resolveRunConfig turns CLI flags into a runConfig. The env runtime image comes
// either from --image (cluster-less) or from the named environment's CRD.
func (opts *RunSubCommand) resolveRunConfig(input cli.Input) (runConfig, error) {
	codePath := input.String(flagkey.PkgCode)
	if codePath == "" {
		return runConfig{}, fmt.Errorf("need --%v: the source file to run", flagkey.PkgCode)
	}

	fnName := input.String(flagkey.FnName)
	if fnName == "" {
		fnName = defaultLocalFunctionName
	}
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return runConfig{}, err
	}

	image := input.String(flagkey.FnImageName)
	envVersion := input.Int(flagkey.FnRunEnvVersion)
	switch {
	case image != "":
		// cluster-less: use the image directly with the supplied --env-version.
	case input.String(flagkey.FnEnvironmentName) != "":
		envName := input.String(flagkey.FnEnvironmentName)
		env, err := opts.Client().FissionClientSet.CoreV1().Environments(namespace).Get(input.Context(), envName, metav1.GetOptions{})
		if err != nil {
			return runConfig{}, fmt.Errorf("read environment %q: %w", envName, err)
		}
		image = env.Spec.Runtime.Image
		envVersion = env.Spec.Version
		if image == "" {
			return runConfig{}, fmt.Errorf("environment %q has no runtime image", envName)
		}
	default:
		return runConfig{}, fmt.Errorf("need --%v (cluster-less) or --%v to resolve the runtime image", flagkey.FnImageName, flagkey.FnEnvironmentName)
	}

	method := http.MethodGet
	if methods := input.StringSlice(flagkey.HtMethod); len(methods) > 0 {
		method = methods[0]
	}

	return runConfig{
		image:        image,
		envVersion:   envVersion,
		entrypoint:   input.String(flagkey.FnEntrypoint),
		codePath:     codePath,
		functionMeta: metav1.ObjectMeta{Name: fnName, Namespace: namespace},
		method:       method,
		headers:      input.StringSlice(flagkey.FnTestHeader),
		body:         input.String(flagkey.FnTestBody),
		subPath:      input.String(flagkey.FnSubPath),
		keep:         input.Bool(flagkey.FnRunKeep),
	}, nil
}

// runLocal drives the full inner loop: lay the source out as the runtime
// expects, start the env runtime container, replay /v2/specialize, invoke the
// function over the shared invoke path, print the response, and tear down
// (unless --keep). It takes a localRuntime so the flow is unit-testable.
func runLocal(ctx context.Context, rt localRuntime, cfg runConfig, stdout, stderr io.Writer) error {
	hostDir, err := prepareSourceMount(cfg.codePath, cfg.envVersion)
	if err != nil {
		return err
	}
	if cfg.keep {
		fmt.Fprintf(stderr, "Keeping mount dir %s\n", hostDir)
	} else {
		defer os.RemoveAll(hostDir)
	}

	hostPort, err := freePort()
	if err != nil {
		return fmt.Errorf("finding a free port: %w", err)
	}

	if err := rt.PullImage(ctx, cfg.image); err != nil {
		return fmt.Errorf("pulling image %q: %w", cfg.image, err)
	}

	fmt.Fprintf(stderr, "Starting %s on 127.0.0.1:%d ...\n", cfg.image, hostPort)
	id, err := rt.StartContainer(ctx, containerSpec{Image: cfg.image, HostDir: hostDir, HostPort: hostPort})
	if err != nil {
		return err
	}
	if cfg.keep {
		fmt.Fprintf(stderr, "Keeping container %s (remove it with: docker rm -f %s)\n", shortID(id), shortID(id))
	} else {
		// Tear down even if ctx was canceled (Ctrl-C) — the container outlives ctx
		// — but bound it so an unresponsive daemon can't hang CLI exit.
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = rt.Stop(stopCtx, id)
		}()
	}

	if err := specialize(ctx, cfg, hostPort); err != nil {
		return fmt.Errorf("specializing function: %w", err)
	}
	return invokeLocal(ctx, cfg, hostPort, stdout, stderr)
}

// specialize replays the env loader contract against the local runtime: a v2+
// env gets the JSON FunctionLoadRequest on /v2/specialize; a v1 env gets the
// text/plain /specialize call. The connect-refused retry covers the window
// between container start and the env server accepting connections.
func specialize(ctx context.Context, cfg runConfig, hostPort int) error {
	client := &http.Client{Timeout: 30 * time.Second}
	url := specializeURL(hostPort, cfg.envVersion)

	if cfg.envVersion < 2 {
		return httpx.PostWithConnRetry(ctx, client, url, "text/plain", nil, logr.Logger{}, specializeMaxRetries, nil)
	}
	payload, err := json.Marshal(buildLoadRequest(&cfg.functionMeta, cfg.entrypoint, cfg.envVersion))
	if err != nil {
		return fmt.Errorf("encoding load request: %w", err)
	}
	return httpx.PostWithConnRetry(ctx, client, url, "application/json", payload, logr.Logger{}, specializeMaxRetries, nil)
}

// invokeLocal calls the specialized function over the same DoHTTPRequest path
// `function test` uses, attaching the X-Fission-Function-* headers and rendering
// the response (or RFC-0015 failure attribution) the same way.
func invokeLocal(ctx context.Context, cfg runConfig, hostPort int, stdout, stderr io.Writer) error {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", hostPort, invokePath(cfg.subPath))
	headers := append(functionHeaders(cfg.functionMeta), cfg.headers...)

	resp, err := DoHTTPRequest(ctx, url, headers, cfg.method, cfg.body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from function: %w", err)
	}

	if reqID := resp.Header.Get(correlation.HeaderRequestID); reqID != "" {
		fmt.Fprintf(stderr, "Request ID: %s\n", reqID)
	}
	if resp.StatusCode >= 400 {
		renderInvocationFailure(stderr, cfg.functionMeta.Name, resp.StatusCode, resp.Header.Get(correlation.HeaderComponent), body)
		return errors.New("error getting function response")
	}
	_, err = stdout.Write(body)
	return err
}

// invokePath normalizes the optional --subpath into the request path.
func invokePath(subPath string) string {
	if subPath == "" {
		return "/"
	}
	if !strings.HasPrefix(subPath, "/") {
		return "/" + subPath
	}
	return subPath
}

// shortID trims a container id to its 12-char short form for display.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
