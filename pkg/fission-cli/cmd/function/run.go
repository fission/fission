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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/error/network"
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
//
// Executor types collapse to two local shapes: poolmgr and newdeploy both run an
// environment runtime image and load code via the specialize contract
// (specialize=true, codePath/envVersion/entrypoint set); container runs the
// user's own server image directly with no specialize and no code mount
// (specialize=false).
type runConfig struct {
	image         string
	containerPort int // in-container port the server listens on (8888 for env runtimes)
	specialize    bool
	envVersion    int
	entrypoint    string
	codePath      string
	functionMeta  metav1.ObjectMeta
	method        string
	headers       []string
	body          string
	subPath       string
	keep          bool
}

type RunSubCommand struct {
	cmd.CommandActioner
}

// Run executes a function locally in Docker (RFC-0018) and invokes it over the
// same path `function test` uses — no cluster round-trip. For poolmgr/newdeploy
// it runs the environment runtime image and replays the specialize contract; for
// the container executor it runs the user's own server image directly.
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

// resolveRunConfig turns CLI flags into a runConfig, dispatching on --executor:
// container runs the user's own --image with no specialize; poolmgr/newdeploy run
// an environment runtime image (from --image or the --env CRD) and specialize.
func (opts *RunSubCommand) resolveRunConfig(input cli.Input) (runConfig, error) {
	fnName := input.String(flagkey.FnName)
	if fnName == "" {
		fnName = defaultLocalFunctionName
	}
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return runConfig{}, err
	}

	method := http.MethodGet
	if methods := input.StringSlice(flagkey.HtMethod); len(methods) > 0 {
		method = methods[0]
	}

	cfg := runConfig{
		functionMeta: metav1.ObjectMeta{Name: fnName, Namespace: namespace},
		method:       method,
		headers:      input.StringSlice(flagkey.FnTestHeader),
		body:         input.String(flagkey.FnTestBody),
		subPath:      input.String(flagkey.FnSubPath),
		keep:         input.Bool(flagkey.FnRunKeep),
	}

	executorType := fv1.ExecutorType(input.String(flagkey.FnExecutorType))
	switch executorType {
	case fv1.ExecutorTypeContainer:
		// The user's container image is the function server; no env, no specialize.
		image := input.String(flagkey.FnImageName)
		if image == "" {
			return runConfig{}, fmt.Errorf("--%v=container requires --%v (the function container image)", flagkey.FnExecutorType, flagkey.FnImageName)
		}
		cfg.image = image
		cfg.containerPort = input.Int(flagkey.FnPort)
		cfg.specialize = false
	case fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy:
		if err := opts.resolveEnvRun(input, namespace, &cfg); err != nil {
			return runConfig{}, err
		}
	default:
		return runConfig{}, fmt.Errorf("unsupported --%v %q (one of poolmgr, newdeploy, container)", flagkey.FnExecutorType, executorType)
	}

	return cfg, nil
}

// resolveEnvRun fills cfg for the env-runtime executors (poolmgr/newdeploy): the
// image comes from --image (cluster-less) or the named environment's CRD, and the
// source is loaded via the specialize contract.
func (opts *RunSubCommand) resolveEnvRun(input cli.Input, namespace string, cfg *runConfig) error {
	codePath := input.String(flagkey.PkgCode)
	if codePath == "" {
		return fmt.Errorf("need --%v: the source file to run", flagkey.PkgCode)
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
			return fmt.Errorf("read environment %q: %w", envName, err)
		}
		image = env.Spec.Runtime.Image
		envVersion = env.Spec.Version
		if image == "" {
			return fmt.Errorf("environment %q has no runtime image", envName)
		}
	default:
		return fmt.Errorf("need --%v (cluster-less) or --%v to resolve the runtime image", flagkey.FnImageName, flagkey.FnEnvironmentName)
	}

	cfg.image = image
	cfg.containerPort = envContainerPort
	cfg.specialize = true
	cfg.envVersion = envVersion
	cfg.entrypoint = input.String(flagkey.FnEntrypoint)
	cfg.codePath = codePath
	return nil
}

// runLocal drives the full inner loop: for env runtimes, lay the source out and
// replay the specialize contract; for container functions, just start the image.
// Either way it then invokes over the shared invoke path, prints the response,
// and tears down (unless --keep). It takes a localRuntime so the flow is
// unit-testable.
func runLocal(ctx context.Context, rt localRuntime, cfg runConfig, stdout, stderr io.Writer) error {
	var hostDir string
	if cfg.specialize {
		var err error
		hostDir, err = prepareSourceMount(cfg.codePath, cfg.envVersion)
		if err != nil {
			return err
		}
		if cfg.keep {
			fmt.Fprintf(stderr, "Keeping mount dir %s\n", hostDir)
		} else {
			defer os.RemoveAll(hostDir)
		}
	}

	hostPort, err := freePort()
	if err != nil {
		return fmt.Errorf("finding a free port: %w", err)
	}

	if err := rt.PullImage(ctx, cfg.image); err != nil {
		return fmt.Errorf("pulling image %q: %w", cfg.image, err)
	}

	fmt.Fprintf(stderr, "Starting %s on 127.0.0.1:%d ...\n", cfg.image, hostPort)
	id, err := rt.StartContainer(ctx, containerSpec{Image: cfg.image, HostDir: hostDir, HostPort: hostPort, ContainerPort: cfg.containerPort})
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

	if cfg.specialize {
		if err := specialize(ctx, cfg, hostPort); err != nil {
			return fmt.Errorf("specializing function: %w", err)
		}
	} else if err := waitForServer(ctx, hostPort); err != nil {
		// The container image is its own server — there is no specialize call to
		// gate readiness, so probe until it accepts a request before invoking.
		return err
	}
	return invokeLocal(ctx, cfg, hostPort, stdout, stderr)
}

// waitForServer probes the container's HTTP server until it responds (any
// status), retrying on the same connection-level failures specialize tolerates
// (refused/dial/reset/EOF) while the server comes up. Used for container-executor
// functions, which have no specialize call to gate readiness.
func waitForServer(ctx context.Context, hostPort int) error {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/", hostPort)
	var lastErr error
	for i := range specializeMaxRetries {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		lastErr = err
		netErr := network.Adapter(err)
		retryable := netErr != nil && (netErr.IsConnRefusedError() || netErr.IsDialError() || netErr.IsConnResetError())
		if !retryable || i == specializeMaxRetries-1 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for function server: %w", ctx.Err())
		case <-time.After(500 * time.Duration(2*i+1) * time.Millisecond):
		}
	}
	return fmt.Errorf("function server did not become ready: %w", lastErr)
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
