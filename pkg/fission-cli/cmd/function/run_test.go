// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fetcher"
	"github.com/fission/fission/pkg/utils/correlation"
)

// TestLoadRequestWireContract is the contract-regression guard (RFC-0018 §6):
// the CLI carries a local copy of the specialize wire struct so it need not
// import the heavy pkg/fetcher. This asserts that local copy marshals
// byte-for-byte the same JSON as the canonical fetcher.FunctionLoadRequest, so
// the local loop can never drift from the in-cluster loader contract.
func TestLoadRequestWireContract(t *testing.T) {
	meta := &metav1.ObjectMeta{Name: "fn", Namespace: "ns", UID: "uid-1", ResourceVersion: "7"}

	local := loadRequest{
		FilePath:         "/userfunc/deployarchive",
		FunctionName:     "main",
		URL:              "",
		FunctionMetadata: meta,
		EnvVersion:       2,
	}
	canonical := fetcher.FunctionLoadRequest{
		FilePath:         "/userfunc/deployarchive",
		FunctionName:     "main",
		URL:              "",
		FunctionMetadata: meta,
		EnvVersion:       2,
	}

	localJSON, err := json.Marshal(local)
	require.NoError(t, err)
	canonicalJSON, err := json.Marshal(canonical)
	require.NoError(t, err)
	assert.JSONEq(t, string(canonicalJSON), string(localJSON),
		"local loadRequest drifted from fetcher.FunctionLoadRequest wire shape")
}

func TestBuildLoadRequest(t *testing.T) {
	meta := &metav1.ObjectMeta{Name: "myfn", Namespace: "default"}

	t.Run("v2 targets deployarchive", func(t *testing.T) {
		lr := buildLoadRequest(meta, "main", 2)
		assert.Equal(t, "/userfunc/deployarchive", lr.FilePath)
		assert.Equal(t, "main", lr.FunctionName)
		assert.Equal(t, 2, lr.EnvVersion)
		assert.Same(t, meta, lr.FunctionMetadata)
	})
	t.Run("v1 targets user", func(t *testing.T) {
		lr := buildLoadRequest(meta, "", 1)
		assert.Equal(t, "/userfunc/user", lr.FilePath)
	})
}

func TestTargetFilename(t *testing.T) {
	assert.Equal(t, targetFilenameUser, targetFilename(1))
	assert.Equal(t, targetFilenameDeploy, targetFilename(2))
	assert.Equal(t, targetFilenameDeploy, targetFilename(3))
}

func TestSpecializeURL(t *testing.T) {
	assert.Equal(t, "http://127.0.0.1:9000/specialize", specializeURL(9000, 1))
	assert.Equal(t, "http://127.0.0.1:9000/v2/specialize", specializeURL(9000, 2))
}

func TestInvokePath(t *testing.T) {
	assert.Equal(t, "/", invokePath(""))
	assert.Equal(t, "/foo", invokePath("foo"))
	assert.Equal(t, "/foo", invokePath("/foo"))
}

func TestFunctionHeaders(t *testing.T) {
	meta := metav1.ObjectMeta{Name: "fn", Namespace: "ns", UID: "u1", ResourceVersion: "9"}
	got := functionHeaders(meta)
	assert.ElementsMatch(t, []string{
		"X-Fission-Function-Uid:u1",
		"X-Fission-Function-Name:fn",
		"X-Fission-Function-Namespace:ns",
		"X-Fission-Function-ResourceVersion:9",
	}, got)
}

func TestShortID(t *testing.T) {
	assert.Equal(t, "abc", shortID("abc"))
	assert.Equal(t, "0123456789ab", shortID("0123456789abcdef"))
}

// fakeRuntime implements localRuntime without Docker: StartContainer binds a
// real HTTP server on the requested host port that emulates the env runtime —
// recording the /v2/specialize body and echoing invocations — so runLocal's
// real httpx specialize call and combinedHTTPRequest invoke path are exercised
// end-to-end.
type fakeRuntime struct {
	echo string // body returned on invoke

	logContent      []byte // stdcopy-framed stream returned by Logs
	builderArtifact string // when set, "/" serves the builder protocol (POST builds)

	mu             sync.Mutex // guards fields written by the server goroutine
	srv            *http.Server
	pulled         []string
	stopped        bool
	lastSpec       containerSpec
	specializePath string
	specializeBody []byte
	invokeHeaders  http.Header
	invokeBody     []byte
}

func (f *fakeRuntime) PullImage(ctx context.Context, image string) error {
	f.pulled = append(f.pulled, image)
	return nil
}

// getSpecializePath / setSpecializePath synchronize access for tests (like
// --watch) that read the field while the server goroutine is still running.
func (f *fakeRuntime) getSpecializePath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specializePath
}

func (f *fakeRuntime) setSpecializePath(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specializePath = s
}

func (f *fakeRuntime) StartContainer(ctx context.Context, spec containerSpec) (string, error) {
	f.lastSpec = spec
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.specializePath = r.URL.Path
		f.specializeBody = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
	mux.HandleFunc("/v2/specialize", record)
	mux.HandleFunc("/specialize", record)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Builder protocol: a POST builds (emulate the builder by creating the
		// artifact dir in the shared mount and returning its name); a GET is the
		// readiness probe.
		if f.builderArtifact != "" {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusOK)
				return
			}
			shared := spec.Mounts[0].HostDir
			_ = os.MkdirAll(filepath.Join(shared, f.builderArtifact), 0o755)
			_ = os.WriteFile(filepath.Join(shared, f.builderArtifact, "built.txt"), []byte("ok"), 0o644)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(buildResponse{ArtifactFilename: f.builderArtifact, BuildLogs: "fake build"})
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.invokeHeaders = r.Header.Clone()
		f.invokeBody = body
		f.mu.Unlock()
		w.Header().Set(correlation.HeaderRequestID, "test-req-1")
		fmt.Fprint(w, f.echo)
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", spec.Ports[0].Host))
	if err != nil {
		return "", err
	}
	f.srv = &http.Server{Handler: mux}
	go func() { _ = f.srv.Serve(ln) }()
	return "fakecontainerid00000", nil
}

func (f *fakeRuntime) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.logContent)), nil
}

func (f *fakeRuntime) Stop(ctx context.Context, id string) error {
	f.stopped = true
	if f.srv != nil {
		return f.srv.Shutdown(ctx)
	}
	return nil
}

func (f *fakeRuntime) Close() error { return nil }

func writeTempCode(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "code.py")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestRunLocalFlow(t *testing.T) {
	f := &fakeRuntime{echo: "hello-from-fn"}
	cfg := runConfig{
		image:         "img:test",
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    2,
		entrypoint:    "main",
		codePath:      writeTempCode(t, "print('hi')"),
		functionMeta:  metav1.ObjectMeta{Name: "myfn", Namespace: "default"},
		method:        http.MethodPost,
		body:          "input-body",
		headers:       []string{"X-Custom:1"},
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), f, cfg, &stdout, &stderr))

	// Response body goes to stdout; the request id is echoed to stderr.
	assert.Equal(t, "hello-from-fn", stdout.String())
	assert.Contains(t, stderr.String(), "Request ID: test-req-1")

	// The image was pulled and the container torn down (keep=false).
	assert.Equal(t, []string{"img:test"}, f.pulled)
	assert.True(t, f.stopped, "container should be stopped when --keep is not set")

	// /v2/specialize received the load request pointing at the bind-mounted source.
	assert.Equal(t, "/v2/specialize", f.specializePath)
	var lr loadRequest
	require.NoError(t, json.Unmarshal(f.specializeBody, &lr))
	assert.Equal(t, "/userfunc/deployarchive", lr.FilePath)
	assert.Equal(t, "main", lr.FunctionName)
	assert.Equal(t, 2, lr.EnvVersion)
	require.NotNil(t, lr.FunctionMetadata)
	assert.Equal(t, "myfn", lr.FunctionMetadata.Name)

	// The invocation carried the function-metadata headers, the user header, and the body.
	assert.Equal(t, "myfn", f.invokeHeaders.Get("X-Fission-Function-Name"))
	assert.Equal(t, "default", f.invokeHeaders.Get("X-Fission-Function-Namespace"))
	assert.Equal(t, "1", f.invokeHeaders.Get("X-Custom"))
	assert.Equal(t, "input-body", string(f.invokeBody))
}

func TestRunLocalV1UsesTextSpecialize(t *testing.T) {
	f := &fakeRuntime{echo: "v1-ok"}
	cfg := runConfig{
		image:         "img:v1",
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    1,
		codePath:      writeTempCode(t, "code"),
		functionMeta:  metav1.ObjectMeta{Name: "fn1", Namespace: "default"},
		method:        http.MethodGet,
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), f, cfg, &stdout, &stderr))

	assert.Equal(t, "v1-ok", stdout.String())
	assert.Equal(t, "/specialize", f.specializePath)
	assert.Empty(t, f.specializeBody, "v1 specialize body is empty")
}

func TestRunLocalKeepLeavesContainer(t *testing.T) {
	f := &fakeRuntime{echo: "ok"}
	// The --keep path intentionally leaves the fake's HTTP server running; reclaim
	// it after the test regardless of assertion outcome.
	t.Cleanup(func() { _ = f.Stop(context.Background(), "") })

	cfg := runConfig{
		image:         "img:test",
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    2,
		codePath:      writeTempCode(t, "x"),
		functionMeta:  metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		method:        http.MethodGet,
		keep:          true,
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), f, cfg, &stdout, &stderr))

	assert.False(t, f.stopped, "container should be left running with --keep")
	assert.Contains(t, stderr.String(), "Keeping container")
}

func TestWriteBindingDirRejectsTraversalKey(t *testing.T) {
	// A key that is a traversing path must be refused, not written outside the dir
	// (defense-in-depth over Kubernetes key validation).
	_, err := writeBindingDir("fission-run-test-", map[string][]byte{"../escape": []byte("x")})
	require.Error(t, err)

	// A normal key still works.
	dir, err := writeBindingDir("fission-run-test-", map[string][]byte{"token": []byte("v")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	got, err := os.ReadFile(filepath.Join(dir, "token"))
	require.NoError(t, err)
	assert.Equal(t, "v", string(got))
}

func TestRunLocalExtractsZipDeploy(t *testing.T) {
	// A zip package (the documented Fission multi-file workflow) is extracted and
	// the extracted directory — not the zip file — is mounted as the deployarchive.
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "pkg.zip")
	zf, err := os.Create(zipPath)
	require.NoError(t, err)
	zw := zip.NewWriter(zf)
	w, err := zw.Create("main.py")
	require.NoError(t, err)
	_, err = w.Write([]byte("def main(): pass"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, zf.Close())

	f := &fakeRuntime{echo: "ok"}
	cfg := runConfig{
		image:         "img:test",
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    2,
		entrypoint:    "main.main",
		codePath:      zipPath,
		functionMeta:  metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		method:        http.MethodGet,
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), f, cfg, &stdout, &stderr))

	require.GreaterOrEqual(t, len(f.lastSpec.Mounts), 1)
	assert.Equal(t, "/userfunc/deployarchive", f.lastSpec.Mounts[0].ContainerDir)
	assert.NotEqual(t, zipPath, f.lastSpec.Mounts[0].HostDir, "the extracted dir, not the zip, must be mounted")
	assert.Contains(t, f.lastSpec.Mounts[0].HostDir, "fission-run-zip-")
}

func TestDumpContainerLogsDemuxes(t *testing.T) {
	f := &fakeRuntime{logContent: stdoutFrame("ModuleNotFoundError: No module named 'x'\n")}
	var buf bytes.Buffer
	dumpContainerLogs(t.Context(), f, "id", &buf)
	assert.Contains(t, buf.String(), "--- container logs ---")
	assert.Contains(t, buf.String(), "ModuleNotFoundError: No module named 'x'")
}

// stdoutFrame wraps payload as a single stdcopy stdout frame (8-byte header:
// stream type + 4-byte big-endian length).
func stdoutFrame(payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = 1 // stdout
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr, []byte(payload)...)
}

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte("# a comment\nFOO=bar\n\n  BAZ=qux  \n"), 0o644))

	got, err := parseEnvFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"FOO=bar", "BAZ=qux"}, got)

	bad := filepath.Join(dir, "bad.env")
	require.NoError(t, os.WriteFile(bad, []byte("NOTAVAR\n"), 0o644))
	_, err = parseEnvFile(bad)
	require.Error(t, err)
}

func TestRunLocalPropagatesEnvAndPorts(t *testing.T) {
	f := &fakeRuntime{echo: "ok"}
	cfg := runConfig{
		image:         "img:test",
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    2,
		codePath:      writeTempCode(t, "x"),
		functionMeta:  metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		method:        http.MethodGet,
		env:           []string{"FOO=bar"},
		debugPort:     5005,
		extraMounts:   []bindMount{{HostDir: t.TempDir(), ContainerDir: "/secrets/default/s1"}},
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), f, cfg, &stdout, &stderr))

	assert.Equal(t, []string{"FOO=bar"}, f.lastSpec.Env)
	// The userfunc mount comes first, then the materialized secret mount.
	require.Len(t, f.lastSpec.Mounts, 2)
	assert.Equal(t, localMountPath, f.lastSpec.Mounts[0].ContainerDir)
	assert.Equal(t, "/secrets/default/s1", f.lastSpec.Mounts[1].ContainerDir)
	// The invoke port plus the debug port are both published.
	require.Len(t, f.lastSpec.Ports, 2)
	assert.Equal(t, envContainerPort, f.lastSpec.Ports[0].Container)
	assert.Equal(t, 5005, f.lastSpec.Ports[1].Container)
	assert.Equal(t, 5005, f.lastSpec.Ports[1].Host)
}

func TestRunLocalWatchReloadsOnChange(t *testing.T) {
	f := &fakeRuntime{echo: "ok"}
	code := writeTempCode(t, "v1")
	cfg := runConfig{
		image:         "img:test",
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    2,
		codePath:      code,
		functionMeta:  metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		method:        http.MethodGet,
		watch:         true,
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- runLocal(ctx, f, cfg, io.Discard, io.Discard) }()

	// Wait for the initial specialize, then edit the source and expect a re-specialize.
	require.Eventually(t, func() bool { return f.getSpecializePath() == "/v2/specialize" }, 3*time.Second, 10*time.Millisecond)
	f.setSpecializePath("")
	require.NoError(t, os.WriteFile(code, []byte("v2"), 0o644))
	require.Eventually(t, func() bool { return f.getSpecializePath() == "/v2/specialize" }, 3*time.Second, 10*time.Millisecond,
		"editing the source should trigger a re-specialize")

	cancel()
	require.NoError(t, <-done)
	assert.True(t, f.stopped)
}

func TestWatchTriggers(t *testing.T) {
	tests := []struct {
		name      string
		ev        fsnotify.Event
		matchFile string
		want      bool
	}{
		{"single-file match", fsnotify.Event{Name: "/src/code.py", Op: fsnotify.Write}, "/src/code.py", true},
		{"single-file other file ignored", fsnotify.Event{Name: "/src/other.py", Op: fsnotify.Write}, "/src/code.py", false},
		{"single-file chmod-only ignored", fsnotify.Event{Name: "/src/code.py", Op: fsnotify.Chmod}, "/src/code.py", false},
		{"dir-mode any source file", fsnotify.Event{Name: "/src/main.go", Op: fsnotify.Write}, "", true},
		{"dir-mode create", fsnotify.Event{Name: "/src/new.go", Op: fsnotify.Create}, "", true},
		{"dir-mode rename", fsnotify.Event{Name: "/src/main.go", Op: fsnotify.Rename}, "", true},
		{"dir-mode vim swap ignored", fsnotify.Event{Name: "/src/.main.go.swp", Op: fsnotify.Write}, "", false},
		{"dir-mode backup ignored", fsnotify.Event{Name: "/src/main.go~", Op: fsnotify.Write}, "", false},
		{"dir-mode emacs lock ignored", fsnotify.Event{Name: "/src/.#main.go", Op: fsnotify.Create}, "", false},
		{"dir-mode tmp ignored", fsnotify.Event{Name: "/src/x.tmp", Op: fsnotify.Write}, "", false},
		{"dir-mode chmod-only ignored", fsnotify.Event{Name: "/src/main.go", Op: fsnotify.Chmod}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, watchTriggers(tc.ev, tc.matchFile))
		})
	}
}

func TestRunLocalWatchDirectoryReloadsOnChange(t *testing.T) {
	// A directory source (a compiled-language project) is watched as a tree:
	// editing any file inside it triggers a re-specialize.
	f := &fakeRuntime{echo: "ok"}
	srcDir := t.TempDir()
	main := filepath.Join(srcDir, "main.go")
	require.NoError(t, os.WriteFile(main, []byte("v1"), 0o644))

	cfg := runConfig{
		image:         "img:test",
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    2,
		entrypoint:    "Handler",
		codePath:      srcDir,
		functionMeta:  metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		method:        http.MethodGet,
		watch:         true,
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- runLocal(ctx, f, cfg, io.Discard, io.Discard) }()

	require.Eventually(t, func() bool { return f.getSpecializePath() == "/v2/specialize" }, 3*time.Second, 10*time.Millisecond)
	f.setSpecializePath("")
	require.NoError(t, os.WriteFile(main, []byte("v2"), 0o644))
	require.Eventually(t, func() bool { return f.getSpecializePath() == "/v2/specialize" }, 3*time.Second, 10*time.Millisecond,
		"editing a file inside the source dir should trigger a re-specialize")

	cancel()
	require.NoError(t, <-done)
}

func TestRunLocalDeployDirectoryIsBindMountedDirectly(t *testing.T) {
	// A multi-file --deploy directory (e.g. a built Next.js app with node_modules)
	// must be bind-mounted directly as /userfunc/deployarchive, not copied.
	deployDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(deployDir, "app.js"), []byte("module.exports=1"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(deployDir, "node_modules"), 0o755))

	f := &fakeRuntime{echo: "ok"}
	cfg := runConfig{
		image:         "fission/node-env:test",
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    2,
		entrypoint:    "app",
		codePath:      deployDir,
		functionMeta:  metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		method:        http.MethodGet,
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), f, cfg, &stdout, &stderr))

	require.GreaterOrEqual(t, len(f.lastSpec.Mounts), 1)
	assert.Equal(t, deployDir, f.lastSpec.Mounts[0].HostDir, "the deploy dir itself should be mounted (no copy)")
	assert.Equal(t, "/userfunc/deployarchive", f.lastSpec.Mounts[0].ContainerDir)
}

func TestRunLocalContainerExecutorSkipsSpecialize(t *testing.T) {
	f := &fakeRuntime{echo: "container-ok"}
	cfg := runConfig{
		image:         "user/myapp:test",
		containerPort: 9000, // a container function's own server port, not 8888
		specialize:    false,
		functionMeta:  metav1.ObjectMeta{Name: "cfn", Namespace: "default"},
		method:        http.MethodPost,
		body:          "payload",
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), f, cfg, &stdout, &stderr))

	// The user image is its own server: invoke runs, but no specialize call is made.
	assert.Equal(t, "container-ok", stdout.String())
	assert.Empty(t, f.specializePath, "container executor must not call specialize")
	assert.Empty(t, f.specializeBody)
	assert.Equal(t, "payload", string(f.invokeBody))
	// Function-metadata headers are still attached for parity with the cluster.
	assert.Equal(t, "cfn", f.invokeHeaders.Get("X-Fission-Function-Name"))
	assert.True(t, f.stopped)
}
