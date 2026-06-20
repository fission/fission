// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"os"
	"os/exec"
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

func TestRunLocalDockerE2E(t *testing.T) {
	if os.Getenv(dockerTestEnvVar) != "1" {
		t.Skipf("set %s=1 (and have a running Docker daemon) to run the Docker-backed e2e test", dockerTestEnvVar)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not found on PATH")
	}

	const image = "fission-rfc0018-fakeenv:test"
	buildFakeEnvImage(t, image)

	rt, err := newDockerRuntime(logr.Logger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	cfg := runConfig{
		image:        image,
		envVersion:   2,
		entrypoint:   "main",
		codePath:     writeTempCode(t, "print('hi')"),
		functionMeta: metav1.ObjectMeta{Name: "myfn", Namespace: "default"},
		method:       "GET",
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runLocal(t.Context(), rt, cfg, &stdout, &stderr))
	assert.Equal(t, "hello-local", stdout.String())
}

func buildFakeEnvImage(t *testing.T, image string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/Dockerfile", []byte(fakeEnvDockerfile), 0o644))

	build := exec.CommandContext(t.Context(), "docker", "build", "-t", image, dir)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rmi", "-f", image).Run()
	})
}
