// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/utils"
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

	// Phase 2 — env bridges + hot reload.
	env   []string // extra runtime env vars (KEY=VALUE), from -e and --env-from
	watch bool     // re-specialize on source change and keep serving

	// Phase 4 — fidelity bridges. Host dirs materialized from cluster
	// Secrets/ConfigMaps, mounted at /secrets/<ns>/<name> and /configs/<ns>/<name>.
	extraMounts []bindMount

	// Phase 5 — debugger. Extra published port for delve/debugpy (0 = none).
	debugPort int

	// Phase 3 — builder leg. When builderImage is set, run it first to compile
	// codePath into a deploy artifact that becomes the runtime's code mount.
	builderImage string
	buildCommand string
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
	// Own the materialized Secret/ConfigMap dirs' lifetime from here, so they are
	// reclaimed on every exit path (including a failed Docker connect) rather than
	// leaking decrypted Secret material in /tmp.
	for _, m := range cfg.extraMounts {
		if cfg.keep {
			note(os.Stderr, "Keeping mount dir %s", m.HostDir)
		} else {
			defer os.RemoveAll(m.HostDir)
		}
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
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return runConfig{}, err
	}
	if namespace == "" {
		// Cluster-less runs have no kubeconfig namespace to fall back to.
		namespace = metav1.NamespaceDefault
	}

	method := http.MethodGet
	if methods := input.StringSlice(flagkey.HtMethod); len(methods) > 0 {
		method = methods[0]
	}

	env, err := runEnv(input)
	if err != nil {
		return runConfig{}, err
	}

	cfg := runConfig{
		functionMeta: metav1.ObjectMeta{Name: fnName, Namespace: namespace},
		method:       method,
		headers:      input.StringSlice(flagkey.FnTestHeader),
		body:         input.String(flagkey.FnTestBody),
		subPath:      input.String(flagkey.FnSubPath),
		keep:         input.Bool(flagkey.FnRunKeep),
		env:          env,
		watch:        input.Bool(flagkey.FnRunWatch),
		debugPort:    input.Int(flagkey.FnRunDebugPort),
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

	if cfg.watch && !cfg.specialize {
		return runConfig{}, fmt.Errorf("--%v is only supported for env executors (poolmgr/newdeploy), not container", flagkey.FnRunWatch)
	}

	// Materialize cluster Secrets/ConfigMaps last, so no validation above can fail
	// after their temp dirs are created (do() owns the cleanup once we return).
	if cfg.extraMounts, err = opts.materializeBindings(input, namespace); err != nil {
		return runConfig{}, err
	}

	return cfg, nil
}

// resolveEnvRun fills cfg for the env-runtime executors (poolmgr/newdeploy): the
// image comes from --image (cluster-less) or the named environment's CRD, and the
// source is loaded via the specialize contract.
func (opts *RunSubCommand) resolveEnvRun(input cli.Input, namespace string, cfg *runConfig) error {
	// The source laid at /userfunc/deployarchive is either a single file (--code,
	// interpreted) or a pre-built deploy directory (--deploy, multi-file apps).
	codePath := input.String(flagkey.PkgCode)
	if deploy := input.StringSlice(flagkey.PkgDeployArchive); len(deploy) > 0 && deploy[0] != "" {
		codePath = deploy[0]
	}
	if codePath == "" {
		return fmt.Errorf("need --%v (a single source file) or --%v (a pre-built directory) to run", flagkey.PkgCode, flagkey.PkgDeployArchive)
	}
	// Resolve to an absolute path: a directory source is bind-mounted, and Docker
	// treats a non-absolute bind source as a named volume (silently mounting an
	// empty volume) rather than the host directory.
	absCodePath, absErr := filepath.Abs(codePath)
	if absErr != nil {
		return fmt.Errorf("resolving source path %q: %w", codePath, absErr)
	}
	codePath = absCodePath

	image := input.String(flagkey.FnImageName)
	envVersion := input.Int(flagkey.FnRunEnvVersion)
	var (
		env *fv1.Environment
		err error
	)
	switch {
	case image != "":
		// cluster-less: use the image directly with the supplied --env-version.
	case input.String(flagkey.FnEnvironmentName) != "":
		if !opts.ClusterAvailable() {
			return fmt.Errorf("--%v needs a Kubernetes cluster to resolve the environment image; run cluster-less with --%v instead", flagkey.FnEnvironmentName, flagkey.FnImageName)
		}
		envName := input.String(flagkey.FnEnvironmentName)
		env, err = opts.Client().FissionClientSet.CoreV1().Environments(namespace).Get(input.Context(), envName, metav1.GetOptions{})
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

	// Phase 3: opt into the builder leg with --build, taking the builder image and
	// command from flags (cluster-less) or the resolved environment.
	if input.Bool(flagkey.FnRunBuild) {
		cfg.builderImage = input.String(flagkey.FnRunBuilderImage)
		cfg.buildCommand = input.String(flagkey.FnBuildCmd)
		if env != nil {
			if cfg.builderImage == "" {
				cfg.builderImage = env.Spec.Builder.Image
			}
			if cfg.buildCommand == "" {
				cfg.buildCommand = env.Spec.Builder.Command
			}
		}
		if cfg.builderImage == "" {
			return fmt.Errorf("--%v needs a builder image: pass --%v or resolve it from --%v", flagkey.FnRunBuild, flagkey.FnRunBuilderImage, flagkey.FnEnvironmentName)
		}
	}
	return nil
}

// runEnv collects the extra runtime env vars from --env-from (a KEY=VALUE file)
// followed by repeated -e flags (which therefore override the file).
func runEnv(input cli.Input) ([]string, error) {
	var env []string
	if file := input.String(flagkey.FnRunEnvFile); file != "" {
		vars, err := parseEnvFile(file)
		if err != nil {
			return nil, err
		}
		env = append(env, vars...)
	}
	return append(env, input.StringSlice(flagkey.FnRunEnvVar)...), nil
}

func parseEnvFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading env file: %w", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			return nil, fmt.Errorf("invalid env file line %q (want KEY=VALUE)", line)
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// materializeBindings reads the referenced cluster Secrets/ConfigMaps and writes
// each into a host temp dir (one file per key), returning bind mounts that place
// them at /secrets/<ns>/<name> and /configs/<ns>/<name> — the same on-disk layout
// the in-cluster fetcher produces.
func (opts *RunSubCommand) materializeBindings(input cli.Input, namespace string) (mounts []bindMount, err error) {
	secrets := input.StringSlice(flagkey.FnSecret)
	cfgMaps := input.StringSlice(flagkey.FnCfgMap)
	if len(secrets) == 0 && len(cfgMaps) == 0 {
		return nil, nil
	}
	if !opts.ClusterAvailable() {
		return nil, fmt.Errorf("--%v/--%v need a Kubernetes cluster to read the referenced objects; omit them to run cluster-less", flagkey.FnSecret, flagkey.FnCfgMap)
	}
	// Don't leave decrypted Secret material on disk if a later read fails.
	defer func() {
		if err != nil {
			for _, m := range mounts {
				_ = os.RemoveAll(m.HostDir)
			}
		}
	}()
	kc := opts.Client().KubernetesClient
	for _, name := range secrets {
		s, err := kc.CoreV1().Secrets(namespace).Get(input.Context(), name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("read secret %q: %w", name, err)
		}
		dir, err := writeBindingDir("fission-run-secret-", s.Data)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, bindMount{HostDir: dir, ContainerDir: filepath.Join(secretsMountPath, namespace, name)})
	}
	for _, name := range cfgMaps {
		c, err := kc.CoreV1().ConfigMaps(namespace).Get(input.Context(), name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("read configmap %q: %w", name, err)
		}
		data := make(map[string][]byte, len(c.Data)+len(c.BinaryData))
		for k, v := range c.Data {
			data[k] = []byte(v)
		}
		maps.Copy(data, c.BinaryData)
		dir, err := writeBindingDir("fission-run-cfgmap-", data)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, bindMount{HostDir: dir, ContainerDir: filepath.Join(configsMountPath, namespace, name)})
	}
	return mounts, nil
}

func writeBindingDir(prefix string, data map[string][]byte) (dir string, err error) {
	dir, err = os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for k, v := range data {
		// RootWriteFile confines the write to dir through an os.Root, so a key that
		// is a traversing path (defense-in-depth over Kubernetes key validation)
		// cannot escape the bind dir.
		if err = utils.RootWriteFile(dir, k, v, 0o644); err != nil {
			return "", fmt.Errorf("writing %q into bind dir: %w", k, err)
		}
	}
	return dir, nil
}

// runLocal drives the full inner loop: optionally compile via the builder leg,
// lay the source out (env runtimes) or run the image as-is (container), replay
// the specialize contract, then either invoke once and tear down, or (with
// --watch) serve and re-specialize on source change. It takes a localRuntime so
// the flow is unit-testable.
func runLocal(ctx context.Context, rt localRuntime, cfg runConfig, stdout, stderr io.Writer) error {
	mounts := append([]bindMount(nil), cfg.extraMounts...)

	// prepare lays the function code out for the runtime; it is reused on every
	// --watch reload (a no-op for a directly-mounted deploy directory).
	prepare := func() error { return nil }
	if cfg.specialize {
		srcMounts, p, cleanup, err := prepareEnvSource(ctx, rt, cfg, stderr)
		if err != nil {
			return err
		}
		defer cleanup()
		prepare = p
		mounts = append(srcMounts, mounts...)
	}

	hostPort, err := utils.FindFreePort()
	if err != nil {
		return fmt.Errorf("finding a free port: %w", err)
	}
	ports := []portMapping{{Host: hostPort, Container: cfg.containerPort}}
	if cfg.debugPort != 0 {
		ports = append(ports, portMapping{Host: cfg.debugPort, Container: cfg.debugPort})
		note(stderr, "Debugger port published on %s:%d", localhostAddr, cfg.debugPort)
	}

	if err := rt.PullImage(ctx, cfg.image); err != nil {
		return fmt.Errorf("pulling image %q: %w", cfg.image, err)
	}

	spec := containerSpec{Image: cfg.image, Mounts: mounts, Ports: ports, Env: cfg.env}

	// launch starts a fresh container and makes it ready: specialize for env
	// runtimes, or wait for a container image's own server. It returns the new
	// container id, tearing the container down if it fails to come up.
	launch := func() (string, error) {
		step(stderr, "Starting %s on %s:%d ...", cfg.image, localhostAddr, hostPort)
		id, err := rt.StartContainer(ctx, spec)
		if err != nil {
			return "", err
		}
		ready, wrap, msg := func() error { return waitForServer(ctx, hostPort) }, func(e error) error { return e }, "Waiting for function server"
		if cfg.specialize {
			ready = func() error { return specialize(ctx, cfg, hostPort) }
			wrap = func(e error) error { return fmt.Errorf("specializing function: %w", e) }
			msg = "Specializing function"
		}
		step(stderr, "%s ...", msg)
		if err := ready(); err != nil {
			dumpContainerLogs(ctx, rt, id, stderr)
			stopQuietly(ctx, rt, id)
			return "", wrap(err)
		}
		return id, nil
	}

	id, err := launch()
	if err != nil {
		return err
	}
	if cfg.keep {
		note(stderr, "Keeping container %s (remove it with: docker rm -f %s)", shortID(id), shortID(id))
	} else {
		// Tear down the current container; --watch replaces id below.
		defer func() { stopQuietly(ctx, rt, id) }()
	}

	if cfg.watch {
		// Env runtimes reject a second /v2/specialize on an already-specialized
		// ("not a generic") container, so a reload replaces the container: it
		// re-prepares the code, stops the old container (freeing the host port),
		// and launches a fresh one on the same port.
		reload := func() error {
			if err := prepare(); err != nil {
				return err
			}
			stopQuietly(ctx, rt, id)
			newID, err := launch()
			if err != nil {
				return err
			}
			id = newID
			return nil
		}
		return serveAndWatch(ctx, cfg, hostPort, reload, stderr)
	}
	// Note: an invoke that returns 4xx/5xx is a normal function response (already
	// rendered by invokeLocal), not a container failure, so we don't dump logs
	// there — only on specialize/startup failures above.
	return invokeLocal(ctx, cfg, hostPort, stdout, stderr)
}

// serveAndWatch prints the local URL, then blocks until the context is canceled,
// re-running reload (rebuild/recopy + re-specialize) whenever the source file
// changes. File-change bursts are debounced so a save triggers one reload.
func serveAndWatch(ctx context.Context, cfg runConfig, hostPort int, reload func() error, stderr io.Writer) error {
	url := fmt.Sprintf("http://%s:%d", localhostAddr, hostPort)
	success(stderr, "Serving %s at %s — watching %s for changes (Ctrl-C to stop)", cfg.functionMeta.Name, url, cfg.codePath)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating file watcher: %w", err)
	}
	defer watcher.Close()

	// A single --code file: watch its parent dir (editors rename on save) and
	// match the one file. A directory source (--deploy, or a builder source like
	// a Go/Java project): watch the whole tree so any file inside triggers a
	// reload, recompiling for builder envs.
	var matchFile string
	if info, serr := os.Stat(cfg.codePath); serr == nil && info.IsDir() {
		if err := addWatchTree(watcher, cfg.codePath); err != nil {
			return fmt.Errorf("watching %s: %w", cfg.codePath, err)
		}
	} else {
		if err := watcher.Add(filepath.Dir(cfg.codePath)); err != nil {
			return fmt.Errorf("watching %s: %w", cfg.codePath, err)
		}
		matchFile = filepath.Clean(cfg.codePath)
	}

	const debounce = 250 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return nil
		case werr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fail(stderr, "watch error: %v", werr)
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if !watchTriggers(ev, matchFile) {
				continue
			}
			// Pick up newly-created subdirectories of a directory source.
			if matchFile == "" && ev.Op&fsnotify.Create != 0 {
				if fi, serr := os.Stat(ev.Name); serr == nil && fi.IsDir() {
					_ = addWatchTree(watcher, ev.Name)
				}
			}
			if !drainBurst(ctx, watcher, debounce) {
				return nil
			}
			if err := reload(); err != nil {
				fail(stderr, "reload failed: %v", err)
				continue
			}
			success(stderr, "reloaded")
		}
	}
}

// watchTriggers reports whether a file event should cause a reload: a relevant op
// on the watched file (single-file mode), or on any non-noise path (dir mode).
func watchTriggers(ev fsnotify.Event, matchFile string) bool {
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	if matchFile != "" {
		return filepath.Clean(ev.Name) == matchFile
	}
	base := filepath.Base(ev.Name)
	// Editor temp/swap files churn during a save; ignore them.
	return !strings.HasSuffix(base, "~") && !strings.HasPrefix(base, ".#") &&
		!strings.HasSuffix(base, ".swp") && !strings.HasSuffix(base, ".tmp")
}

// addWatchTree watches dir and its subdirectories, skipping VCS / dependency /
// build-output dirs that would add thousands of irrelevant watches and churn.
func addWatchTree(watcher *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		switch d.Name() {
		case ".git", "node_modules", "vendor", "target", ".next", "__pycache__":
			if p != dir {
				return filepath.SkipDir
			}
		}
		return watcher.Add(p)
	})
}

// drainBurst waits for the file-event burst to settle: it resets a timer on each
// new event and returns true once the source is quiet for the debounce window,
// or false if the context is canceled first.
func drainBurst(ctx context.Context, watcher *fsnotify.Watcher, debounce time.Duration) bool {
	timer := time.NewTimer(debounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-watcher.Events:
			timer.Reset(debounce)
		case <-timer.C:
			return true
		}
	}
}

// waitForServer probes the container's HTTP server until it responds, used for
// container-executor functions which have no specialize call to gate readiness.
func waitForServer(ctx context.Context, hostPort int) error {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://%s:%d/", localhostAddr, hostPort)
	if err := httpx.WaitReady(ctx, client, url, specializeMaxRetries); err != nil {
		return fmt.Errorf("function server did not become ready: %w", err)
	}
	return nil
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

// invokeLocal calls the specialized function over the same combinedHTTPRequest
// path `function test` uses, attaching the X-Fission-Function-* headers and
// rendering the response (or RFC-0015 failure attribution) the same way. It
// talks directly to a locally-specialized pod, not the router internal
// listener, so it attaches the bearer token (not HMAC) and sets no
// invoke-mode header.
func invokeLocal(ctx context.Context, cfg runConfig, hostPort int, stdout, stderr io.Writer) error {
	url := fmt.Sprintf("http://%s:%d%s", localhostAddr, hostPort, invokePath(cfg.subPath))
	headers := append(functionHeaders(cfg.functionMeta), cfg.headers...)

	resp, err := combinedHTTPRequest(ctx, invokeOptions{
		Method:            cfg.method,
		URL:               url,
		Body:              cfg.body,
		Headers:           headers,
		SignWithHMAC:      false,
		AttachBearerToken: true,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from function: %w", err)
	}

	if reqID := resp.Header.Get(correlation.HeaderRequestID); reqID != "" {
		note(stderr, "Request ID: %s", reqID)
	}
	if resp.StatusCode >= 400 {
		renderInvocationFailure(stderr, cfg.functionMeta.Name, resp.StatusCode, resp.Header.Get(correlation.HeaderComponent), body)
		return errors.New("error getting function response")
	}
	_, err = stdout.Write(body)
	return err
}

// prepareEnvSource lays out the function code for an env runtime: it extracts a
// zip package, then either bind-mounts a deploy directory directly (large dirs
// like node_modules are not copied — the kubelet image-volume path mounts a
// package dir the same way) or copies a single --code file / builder artifact
// into a temp userfunc dir. It returns the bind mounts to prepend, a prepare
// closure rerun on every --watch reload (a no-op for a direct directory mount),
// and a cleanup for any temp dir it created.
func prepareEnvSource(ctx context.Context, rt localRuntime, cfg runConfig, stderr io.Writer) (mounts []bindMount, prepare func() error, cleanup func(), err error) {
	// Accumulate temp-dir cleanups; unwind them on an error return, hand them to
	// the caller on success. mkTemp registers each dir so no error path has to
	// remember to clean up by hand.
	var cleanups []func()
	cleanup = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	mkTemp := func(prefix, label string) (dir string) {
		var c func()
		if dir, c, err = tempDir(prefix, label, cfg.keep, stderr); err == nil {
			cleanups = append(cleanups, c)
		}
		return dir
	}
	userfuncMount := func(dir string) []bindMount {
		return []bindMount{{HostDir: dir, ContainerDir: localMountPath}}
	}

	// Build leg: compile the source into a temp userfunc dir (re-built on reload).
	// There is no zip-extract or direct directory mount.
	if cfg.builderImage != "" {
		dir := mkTemp("fission-run-", "mount dir")
		if err != nil {
			return nil, nil, nil, err
		}
		target := filepath.Join(dir, targetFilename(cfg.envVersion))
		prepare = func() error { return runBuilder(ctx, rt, cfg, target, stderr) }
		if err = prepare(); err != nil {
			return nil, nil, nil, fmt.Errorf("preparing function code: %w", err)
		}
		return userfuncMount(dir), prepare, cleanup, nil
	}

	// Source leg: the Fission convention packages a multi-file function as a zip;
	// extract it (zip-slip-safe, like the fetcher) and treat it as a directory.
	source := cfg.codePath
	if isZip, _ := utils.IsZip(ctx, source); isZip {
		dir := mkTemp("fission-run-zip-", "extracted dir")
		if err != nil {
			return nil, nil, nil, err
		}
		if err = utils.Unarchive(ctx, source, dir); err != nil {
			return nil, nil, nil, fmt.Errorf("extracting %q: %w", source, err)
		}
		source = dir
	}

	var info os.FileInfo
	if info, err = os.Stat(source); err != nil {
		return nil, nil, nil, fmt.Errorf("reading source %q: %w", source, err)
	}
	// A pre-built deploy directory (--deploy or an extracted zip) is bind-mounted
	// live; --watch only re-specializes.
	if info.IsDir() {
		deployTarget := filepath.Join(localMountPath, targetFilename(cfg.envVersion))
		return []bindMount{{HostDir: source, ContainerDir: deployTarget}}, func() error { return nil }, cleanup, nil
	}

	// A single --code file: copy it into a temp userfunc dir, re-copied on reload.
	dir := mkTemp("fission-run-", "mount dir")
	if err != nil {
		return nil, nil, nil, err
	}
	target := filepath.Join(dir, targetFilename(cfg.envVersion))
	prepare = func() error { return copyFile(source, target, 0o644) }
	if err = prepare(); err != nil {
		return nil, nil, nil, fmt.Errorf("preparing function code: %w", err)
	}
	return userfuncMount(dir), prepare, cleanup, nil
}

// tempDir creates a temp dir and returns a cleanup that removes it — unless keep
// is set, in which case it prints the path for the developer and the cleanup is
// a no-op. The cleanup decision thus lives in exactly one place.
func tempDir(prefix, label string, keep bool, stderr io.Writer) (string, func(), error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, fmt.Errorf("creating %s: %w", label, err)
	}
	if keep {
		note(stderr, "Keeping %s %s", label, dir)
		return dir, func() {}, nil
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
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
