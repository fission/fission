// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

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
// real httpx specialize call and DoHTTPRequest invoke path are exercised
// end-to-end.
type fakeRuntime struct {
	echo string // body returned on invoke

	srv            *http.Server
	pulled         []string
	stopped        bool
	specializePath string
	specializeBody []byte
	invokeHeaders  http.Header
	invokeBody     []byte
}

func (f *fakeRuntime) PullImage(ctx context.Context, image string) error {
	f.pulled = append(f.pulled, image)
	return nil
}

func (f *fakeRuntime) StartContainer(ctx context.Context, spec containerSpec) (string, error) {
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request) {
		f.specializePath = r.URL.Path
		f.specializeBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}
	mux.HandleFunc("/v2/specialize", record)
	mux.HandleFunc("/specialize", record)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.invokeHeaders = r.Header.Clone()
		f.invokeBody, _ = io.ReadAll(r.Body)
		w.Header().Set(correlation.HeaderRequestID, "test-req-1")
		fmt.Fprint(w, f.echo)
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", spec.HostPort))
	if err != nil {
		return "", err
	}
	f.srv = &http.Server{Handler: mux}
	go func() { _ = f.srv.Serve(ln) }()
	return "fakecontainerid00000", nil
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
		image:        "img:test",
		envVersion:   2,
		entrypoint:   "main",
		codePath:     writeTempCode(t, "print('hi')"),
		functionMeta: metav1.ObjectMeta{Name: "myfn", Namespace: "default"},
		method:       http.MethodPost,
		body:         "input-body",
		headers:      []string{"X-Custom:1"},
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
		image:        "img:v1",
		envVersion:   1,
		codePath:     writeTempCode(t, "code"),
		functionMeta: metav1.ObjectMeta{Name: "fn1", Namespace: "default"},
		method:       http.MethodGet,
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
		image:        "img:test",
		envVersion:   2,
		codePath:     writeTempCode(t, "x"),
		functionMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		method:       http.MethodGet,
		keep:         true,
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), f, cfg, &stdout, &stderr))

	assert.False(t, f.stopped, "container should be left running with --keep")
	assert.Contains(t, stderr.String(), "Keeping container")
}
