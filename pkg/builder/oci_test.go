/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package builder

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// withSrcArchive writes a stub source archive into a fresh shared volume
// directory and returns its path + filename. The OCIHandler short-circuits
// when the archive does not exist, so callers that exercise the build path
// must populate the volume first.
func withSrcArchive(t *testing.T) (volumePath, filename string) {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "fission-builder-oci-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	filename = "src.tar.gz"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("dummy"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	return dir, filename
}

func TestParseDigestFromLogs(t *testing.T) {
	for _, tc := range []struct {
		name string
		logs string
		want string
	}{
		{name: "marker absent", logs: "build complete\n", want: ""},
		{name: "marker present", logs: "step 1 done\nmanifest-digest: sha256:abc123\nstep 2 done\n", want: "sha256:abc123"},
		{name: "trailing whitespace trimmed", logs: "manifest-digest:   sha256:def456   \n", want: "sha256:def456"},
		{name: "first occurrence wins", logs: "manifest-digest: sha256:111\nmanifest-digest: sha256:222\n", want: "sha256:111"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDigestFromLogs(tc.logs); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOCIHandler_RejectsNonPost(t *testing.T) {
	dir, _ := withSrcArchive(t)
	b := MakeBuilder(loggerfactory.GetLogger(), dir)
	req := httptest.NewRequest(http.MethodGet, OCIBuildEndpoint, nil)
	w := httptest.NewRecorder()
	b.OCIHandler(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestOCIHandler_BadJSON(t *testing.T) {
	dir, _ := withSrcArchive(t)
	b := MakeBuilder(loggerfactory.GetLogger(), dir)
	req := httptest.NewRequest(http.MethodPost, OCIBuildEndpoint, bytes.NewReader([]byte("{not json")))
	w := httptest.NewRecorder()
	b.OCIHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestOCIHandler_MissingFields(t *testing.T) {
	dir, _ := withSrcArchive(t)
	b := MakeBuilder(loggerfactory.GetLogger(), dir)
	body, _ := json.Marshal(OCIBuildRequest{ImageRef: ""}) // both required fields empty
	req := httptest.NewRequest(http.MethodPost, OCIBuildEndpoint, bytes.NewReader(body))
	w := httptest.NewRecorder()
	b.OCIHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), "imageRef") {
		t.Fatalf("response must mention imageRef: %s", w.Body.String())
	}
}

func TestOCIHandler_MissingSource(t *testing.T) {
	dir, _ := withSrcArchive(t)
	b := MakeBuilder(loggerfactory.GetLogger(), dir)
	body, _ := json.Marshal(OCIBuildRequest{
		SrcPkgFilename: "does-not-exist.tar.gz",
		ImageRef:       "ghcr.io/example/hello:v1",
	})
	req := httptest.NewRequest(http.MethodPost, OCIBuildEndpoint, bytes.NewReader(body))
	w := httptest.NewRecorder()
	b.OCIHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestOCIHandler_NoBuildKitBinary asserts the handler surfaces 501 when
// the buildkit-build binary is not on PATH. This is the failure mode
// users hit when running with the legacy builder image; a clear status
// code lets them distinguish "image is wrong" from "build failed".
func TestOCIHandler_NoBuildKitBinary(t *testing.T) {
	dir, filename := withSrcArchive(t)
	b := MakeBuilder(loggerfactory.GetLogger(), dir)

	// Force PATH to an empty directory so exec.LookPath fails.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	body, _ := json.Marshal(OCIBuildRequest{
		SrcPkgFilename: filename,
		ImageRef:       "ghcr.io/example/hello:v1",
		BaseImage:      "ghcr.io/fission/node-env-22",
	})
	req := httptest.NewRequest(http.MethodPost, OCIBuildEndpoint, bytes.NewReader(body))
	w := httptest.NewRecorder()
	b.OCIHandler(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d (buildkit-build absent)", w.Code, http.StatusNotImplemented)
	}
	if !strings.Contains(w.Body.String(), "buildkit-build") {
		t.Fatalf("response must mention buildkit-build: %s", w.Body.String())
	}
}
