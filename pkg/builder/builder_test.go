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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestBuilder(t *testing.T) {
	logger := loggerfactory.GetLogger()

	dir, err := os.MkdirTemp("/tmp", "fission-builder-test")
	require.NoError(t, err)
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
				name: "should reject build command containing shell metacharacters",
				buildRequest: &PackageBuildRequest{
					SrcPkgFilename: "test2",
					BuildCommand:   "ps -ef | grep fission",
				},
				expected: &PackageBuildResponse{
					ArtifactFilename: "test2",
					BuildLogs:        "",
				},
				status: http.StatusBadRequest,
			},
			{
				name: "should reject build command with command-substitution",
				buildRequest: &PackageBuildRequest{
					SrcPkgFilename: "test2sub",
					BuildCommand:   "echo $(whoami)",
				},
				expected: &PackageBuildResponse{
					ArtifactFilename: "test2sub",
					BuildLogs:        "",
				},
				status: http.StatusBadRequest,
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
				require.NoError(t, err)
				defer srcFile.Close()
				body, err := json.Marshal(test.buildRequest)
				require.NoError(t, err)
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
				builder.Handler(w, r)
				resp := w.Result()
				if resp.StatusCode != test.status {
					t.Errorf("expected status code %d, got %d", test.status, resp.StatusCode)
				}
				body, err = io.ReadAll(resp.Body)
				require.NoError(t, err)
				var buildResp PackageBuildResponse
				err = json.Unmarshal(body, &buildResp)
				require.NoError(t, err)
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
				require.NoError(t, err)
				srcFile, err := os.Create(dir + "/" + test.srcPkgFilename)
				require.NoError(t, err)
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

		// Names whose resolved absolute path escapes the shared volume root
		// must be rejected with 400 before any filesystem operation. (Names
		// like "foo/../bar" that filepath.Clean resolves to a legitimate
		// in-volume path are not traversal and remain allowed.)
		for _, name := range []string{"../etc/passwd", "../../absolute"} {
			t.Run("should reject traversal name "+name, func(t *testing.T) {
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodDelete, "/clean?name="+url.QueryEscape(name), http.NoBody)
				builder.Clean(w, r)
				resp := w.Result()
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("name %q: want 400, got %d", name, resp.StatusCode)
				}
			})
		}
	})
}

func TestResolveBuildCommand(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantCmd string
		wantArg []string
		wantErr bool
	}{
		{name: "empty falls back to default", input: "", wantCmd: defaultBuildCommand, wantArg: nil},
		{name: "single command", input: "ls", wantCmd: "ls"},
		{name: "command with args", input: "ls -la /tmp", wantCmd: "ls", wantArg: []string{"-la", "/tmp"}},
		{name: "extra whitespace is collapsed", input: "  ls   -la  ", wantCmd: "ls", wantArg: []string{"-la"}},
		{name: "rejects pipe", input: "ls | grep .", wantErr: true},
		{name: "rejects semicolon", input: "ls; rm -rf /", wantErr: true},
		{name: "rejects backtick", input: "ls `whoami`", wantErr: true},
		{name: "rejects command-substitution", input: "ls $(whoami)", wantErr: true},
		{name: "rejects redirect", input: "ls > /etc/passwd", wantErr: true},
		{name: "rejects newline", input: "ls\nrm /", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, args, err := resolveBuildCommand(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got cmd=%q args=%v", cmd, args)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cmd != tc.wantCmd {
				t.Errorf("cmd: want %q, got %q", tc.wantCmd, cmd)
			}
			if len(args) != len(tc.wantArg) {
				t.Errorf("args: want %v, got %v", tc.wantArg, args)
				return
			}
			for i := range args {
				if args[i] != tc.wantArg[i] {
					t.Errorf("args[%d]: want %q, got %q", i, tc.wantArg[i], args[i])
				}
			}
		})
	}
}

func TestSanitizeBuildLogLine(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"with\rcr", `with\rcr`},
		{"with\nlf", `with\nlf`},
		{"both\r\n", `both\r\n`},
		{"", ""},
	}
	for _, tc := range tests {
		if got := sanitizeBuildLogLine(tc.in); got != tc.want {
			t.Errorf("sanitizeBuildLogLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuilderBuild_StripsCRLFFromBuildOutput(t *testing.T) {
	// Build script that emits a payload containing CR/LF — simulates a
	// hostile build script trying to inject fake log lines into the
	// builder's stdout.
	script := "#!/bin/sh\nprintf 'real-line\\nFAKE\\rINJECTED\\n'\n"
	dir := t.TempDir()
	buildScript := filepath.Join(dir, "build.sh")
	if err := os.WriteFile(buildScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcPath, 0o755); err != nil {
		t.Fatal(err)
	}
	dstPath := filepath.Join(dir, "deploy.zip")

	// Capture stdout for the duration of the build call.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	b := MakeBuilder(loggerfactory.GetLogger(), dir)
	logs, err := b.build(context.Background(), buildScript, nil, srcPath, dstPath)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_ = w.Close()
	out, _ := io.ReadAll(r)

	// Embedded CR must not appear as a literal control char in captured stdout —
	// it should be escaped to "\r" (literal backslash-r).
	if bytes.Contains(out, []byte("FAKE\rINJECTED")) {
		t.Fatalf("stdout contains unsanitised CR: %q", out)
	}
	if !bytes.Contains(out, []byte(`FAKE\rINJECTED`)) {
		t.Fatalf("stdout missing escaped form: %q", out)
	}
	// buildLogs (returned to caller) is also sanitised.
	if !strings.Contains(logs, `FAKE\rINJECTED`) {
		t.Fatalf("buildLogs missing escaped form: %q", logs)
	}
}
