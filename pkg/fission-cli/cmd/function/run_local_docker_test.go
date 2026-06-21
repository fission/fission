// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// dockerTestEnvVar gates the Docker-backed e2e test. It is OFF in normal CI
// (which never sets it) and runnable locally with a Docker daemon:
//
//	FISSION_RUN_DOCKER_TESTS=1 go test -run TestRunLocalDockerE2E ./pkg/fission-cli/cmd/function/
const dockerTestEnvVar = "FISSION_RUN_DOCKER_TESTS"

// fakeEnvDockerfile is a stand-in for a real Fission env runtime image: a server
// whose CMD listens on 8888 and 2xx-accepts both /v2/specialize and the
// invocation, echoing a fixed body — enough to drive the real dockerRuntime
// (image pull/create/start, bind-mount, port publish, teardown) end-to-end.
const fakeEnvDockerfile = `FROM python:3.12-alpine
RUN printf '%s\n' \
  'from http.server import BaseHTTPRequestHandler, HTTPServer' \
  'class H(BaseHTTPRequestHandler):' \
  '    def _ok(self):' \
  '        self.send_response(200); self.end_headers()' \
  '    def do_GET(self):' \
  '        self._ok(); self.wfile.write(b"hello-local")' \
  '    def do_POST(self):' \
  '        self._ok(); self.wfile.write(b"hello-local")' \
  'HTTPServer(("0.0.0.0", 8888), H).serve_forever()' > /server.py
CMD ["python", "/server.py"]
`

// fakeContainerDockerfile stands in for a container-executor function image: its
// own server on a non-8888 port that simply echoes, with no specialize endpoint —
// exercising the container path (no bind mount, no specialize, custom port).
const fakeContainerDockerfile = `FROM python:3.12-alpine
RUN printf '%s\n' \
  'from http.server import BaseHTTPRequestHandler, HTTPServer' \
  'class H(BaseHTTPRequestHandler):' \
  '    def _ok(self):' \
  '        self.send_response(200); self.end_headers()' \
  '    def do_GET(self):' \
  '        self._ok(); self.wfile.write(b"container-local")' \
  '    def do_POST(self):' \
  '        self._ok(); self.wfile.write(b"container-local")' \
  'HTTPServer(("0.0.0.0", 9000), H).serve_forever()' > /server.py
CMD ["python", "/server.py"]
`

func requireDocker(t *testing.T) {
	t.Helper()
	if os.Getenv(dockerTestEnvVar) != "1" {
		t.Skipf("set %s=1 (and have a running Docker daemon) to run the Docker-backed e2e test", dockerTestEnvVar)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not found on PATH")
	}
}

func TestRunLocalDockerE2E(t *testing.T) {
	requireDocker(t)

	const image = "fission-rfc0018-fakeenv:test"
	buildFakeImage(t, image, fakeEnvDockerfile)

	rt, err := newDockerRuntime(logr.Logger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	cfg := runConfig{
		image:         image,
		containerPort: envContainerPort,
		specialize:    true,
		envVersion:    2,
		entrypoint:    "main",
		codePath:      writeTempCode(t, "print('hi')"),
		functionMeta:  metav1.ObjectMeta{Name: "myfn", Namespace: "default"},
		method:        "GET",
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), rt, cfg, &stdout, &stderr))
	assert.Equal(t, "hello-local", stdout.String())
}

func TestRunLocalDockerContainerExecutorE2E(t *testing.T) {
	requireDocker(t)

	const image = "fission-rfc0018-fakecontainer:test"
	buildFakeImage(t, image, fakeContainerDockerfile)

	rt, err := newDockerRuntime(logr.Logger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	cfg := runConfig{
		image:         image,
		containerPort: 9000, // the function's own server port, not the env 8888
		specialize:    false,
		functionMeta:  metav1.ObjectMeta{Name: "cfn", Namespace: "default"},
		method:        "GET",
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), rt, cfg, &stdout, &stderr))
	assert.Equal(t, "container-local", stdout.String())
}

// fakeBuilderDockerfile stands in for an env builder image: like a real builder
// image, its default CMD does NOT start the server — the server lives at
// /builder, which run-local invokes as "/builder /packages" (matching
// buildermgr). The server on 8001 implements the build protocol: it copies the
// staged source (/packages/<srcPkgFilename>) to an artifact dir and returns it.
const fakeBuilderDockerfile = `FROM python:3.12-alpine
RUN printf '%s\n' \
  'import json, os, shutil' \
  'from http.server import BaseHTTPRequestHandler, HTTPServer' \
  'class H(BaseHTTPRequestHandler):' \
  '    def do_POST(self):' \
  '        n = int(self.headers.get("Content-Length", 0))' \
  '        req = json.loads(self.rfile.read(n) or b"{}")' \
  '        art = req["srcPkgFilename"] + "-out"' \
  '        shutil.copytree(os.path.join("/packages", req["srcPkgFilename"]), os.path.join("/packages", art))' \
  '        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()' \
  '        self.wfile.write(json.dumps({"artifactFilename": art, "buildLogs": "fake build ok"}).encode())' \
  'HTTPServer(("0.0.0.0", 8001), H).serve_forever()' > /builder.py
RUN printf '%s\n' '#!/bin/sh' 'exec python /builder.py' > /builder && chmod +x /builder
CMD ["python3"]
`

func TestRunBuilderDockerE2E(t *testing.T) {
	requireDocker(t)

	const image = "fission-rfc0018-fakebuilder:test"
	buildFakeImage(t, image, fakeBuilderDockerfile)

	rt, err := newDockerRuntime(logr.Logger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	// Source directory the builder compiles.
	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(srcDir+"/main.go", []byte("package main"), 0o644))

	dst := filepath.Join(t.TempDir(), "deployarchive")
	cfg := runConfig{builderImage: image, codePath: srcDir, buildCommand: "go build"}

	var stderr bytes.Buffer
	require.NoError(t, runBuilder(t.Context(), rt, cfg, dst, &stderr))

	// The artifact (the staged source) was collected to dst.
	got, err := os.ReadFile(filepath.Join(dst, "main.go"))
	require.NoError(t, err)
	assert.Equal(t, "package main", string(got))
	assert.Contains(t, stderr.String(), "fake build ok")
}

func buildFakeImage(t *testing.T, image, dockerfile string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/Dockerfile", []byte(dockerfile), 0o644))

	build := exec.CommandContext(t.Context(), "docker", "build", "-t", image, dir)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rmi", "-f", image).Run()
	})
}
