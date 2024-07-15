/*
Copyright 2022 The Fission Authors.

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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestBuilder(t *testing.T) {
	logger := loggerfactory.GetLogger()

	dir, err := os.MkdirTemp("/tmp", "fission-builder-test")
	if err != nil {
		t.Fatal(err)
	}
	builder := MakeBuilder(logger, dir)

	// Test VersionHandler
	t.Run("VersionHandler", func(t *testing.T) {
		t.Run("should return version", func(t *testing.T) {
			w := &httptest.ResponseRecorder{}
			r := &http.Request{}
			builder.VersionHandler(w, r)
			if w.Result().StatusCode != http.StatusOK {
				t.Errorf("expected status code %d, got %d", http.StatusOK, w.Result().StatusCode)
			}
			if w.Result().Body == nil {
				t.Error("expected body, got nil")
			}
		})
	})

	// Test BuildHandler
	t.Run("BuildHandler", func(t *testing.T) {

		for _, test := range []struct {
			name         string
			buildRequest *PackageBuildRequest
			expected     *PackageBuildResponse
			status       int
		}{
			{
				name: "should work with build command without argument",
				buildRequest: &PackageBuildRequest{
					SrcPkgFilename: "test",
					BuildCommand:   "ls",
				},
				expected: &PackageBuildResponse{
					ArtifactFilename: "test",
					BuildLogs:        "",
				},
				status: http.StatusOK,
			},
			{
				name: "should work with build command with argument",
				buildRequest: &PackageBuildRequest{
					SrcPkgFilename: "test1",
					BuildCommand:   "ls -la",
				},
				expected: &PackageBuildResponse{
					ArtifactFilename: "test1",
					BuildLogs:        "",
				},
				status: http.StatusOK,
			},
			{
				name: "should fail with argument and pipe",
				buildRequest: &PackageBuildRequest{
					SrcPkgFilename: "test2",
					BuildCommand:   "ps -ef | grep fission",
				},
				expected: &PackageBuildResponse{
					ArtifactFilename: "test2",
					BuildLogs:        "",
				},
				status: http.StatusInternalServerError,
			},
			{
				name: "should fail with invalid build command",
				buildRequest: &PackageBuildRequest{
					SrcPkgFilename: "test3",
					BuildCommand:   "lsalas -la",
				},
				expected: &PackageBuildResponse{
					ArtifactFilename: "test3",
					BuildLogs:        "",
				},
				status: http.StatusInternalServerError,
			},
			{
				name: "should fail with valid command and invalid argument",
				buildRequest: &PackageBuildRequest{
					SrcPkgFilename: "test3",
					BuildCommand:   "ls --la",
				},
				expected: &PackageBuildResponse{
					ArtifactFilename: "test3",
					BuildLogs:        "",
				},
				status: http.StatusInternalServerError,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				srcFile, err := os.Create(dir + "/" + test.buildRequest.SrcPkgFilename)
				if err != nil {
					t.Fatal(err)
				}
				defer srcFile.Close()
				body, err := json.Marshal(test.buildRequest)
				if err != nil {
					t.Fatal(err)
				}
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
				builder.Handler(w, r)
				resp := w.Result()
				if resp.StatusCode != test.status {
					t.Errorf("expected status code %d, got %d", test.status, resp.StatusCode)
				}
				body, err = io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				var buildResp PackageBuildResponse
				err = json.Unmarshal(body, &buildResp)
				if err != nil {
					t.Fatal(err)
				}
				if test.status == http.StatusOK {
					if strings.Contains(buildResp.BuildLogs, "error") {
						t.Errorf("expected build logs to not contain error, got %s", buildResp.BuildLogs)
					}
					artifacts := strings.Split(buildResp.ArtifactFilename, "-")
					if len(artifacts) != 2 {
						t.Errorf("expected artifact filename to be of the form <pkgname>-<build-id>, got %s", buildResp.ArtifactFilename)
					}
					if artifacts[0] != test.expected.ArtifactFilename {
						t.Errorf("expected artifact filename to be %s, got %s", test.expected.ArtifactFilename, artifacts[0])
					}
				} else {
					if !strings.Contains(buildResp.BuildLogs, "error") {
						t.Errorf("expected build logs to contain error, got %s", buildResp.BuildLogs)
					}
				}

			})
		}
	})

	// Test CleanHandler
	t.Run("CleanHandler", func(t *testing.T) {
		for _, test := range []struct {
			name           string
			srcPkgFilename string
			handler        func(w http.ResponseWriter, r *http.Request)
			status         int
		}{
			{
				name:           "should fail deleting src pkg: invalid shared volume path",
				srcPkgFilename: "test2",
				handler: func(w http.ResponseWriter, r *http.Request) {
					builder.Clean(w, r)
				},
				status: http.StatusInternalServerError,
			},
			{
				name:           "should fail deleting src pkg: method not allowed",
				srcPkgFilename: "test3",
				handler: func(w http.ResponseWriter, r *http.Request) {
					builder.Handler(w, r)
				},
				status: http.StatusMethodNotAllowed,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				_, err := os.MkdirTemp(dir, test.srcPkgFilename)
				if err != nil {
					t.Fatal(err)
				}
				srcFile, err := os.Create(dir + "/" + test.srcPkgFilename)
				if err != nil {
					t.Fatal(err)
				}
				defer srcFile.Close()
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodDelete, "/clean/"+test.srcPkgFilename, http.NoBody)
				test.handler(w, r)
				resp := w.Result()
				if resp.StatusCode != test.status {
					t.Errorf("expected status code %d, got %d", test.status, resp.StatusCode)
				}
			})
		}
	})
}
