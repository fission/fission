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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_MissingEnvErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{name: "context missing", env: map[string]string{envImageRef: "x", envBaseImage: "y"}},
		{name: "image ref missing", env: map[string]string{envContext: "x", envBaseImage: "y"}},
		{name: "base image missing", env: map[string]string{envContext: "x", envImageRef: "y"}},
		{name: "all missing", env: map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			get := func(k string) string { return tc.env[k] }
			err := run(nil, get, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "must all be set") {
				t.Fatalf("error %q must mention required env vars", err.Error())
			}
		})
	}
}

func TestRun_MissingContextErrors(t *testing.T) {
	get := func(k string) string {
		switch k {
		case envContext:
			return "/no/such/path/exists"
		case envImageRef:
			return "ghcr.io/example:v1"
		case envBaseImage:
			return "ghcr.io/fission/node-env-22"
		}
		return ""
	}
	err := run(nil, get, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Fatalf("error %q must mention context", err.Error())
	}
}

func TestWriteDockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := writeDockerfile(dir, "ghcr.io/fission/node-env-22"); err != nil {
		t.Fatalf("writeDockerfile: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, "FROM ghcr.io/fission/node-env-22") {
		t.Fatalf("Dockerfile missing FROM line: %s", body)
	}
	if !strings.Contains(body, "COPY . /userfunc/") {
		t.Fatalf("Dockerfile must copy source into /userfunc/: %s", body)
	}
}

// TestBuildctlArgs locks down the exact argument layout — the contract
// surface buildctl itself depends on. Catching a regression here is
// easier than debugging a silently-broken build pod in production.
func TestBuildctlArgs(t *testing.T) {
	args := buildctlArgs("/work/ctx", "/tmp/df", "ghcr.io/x:v1", "/tmp/df/metadata.json")
	wantStarts := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=/work/ctx",
		"--local", "dockerfile=/tmp/df",
		"--output", "type=image,name=ghcr.io/x:v1,push=true",
		"--metadata-file", "/tmp/df/metadata.json",
	}
	if len(args) < len(wantStarts) {
		t.Fatalf("args too short: %v", args)
	}
	for i, want := range wantStarts {
		if args[i] != want {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want)
		}
	}
}

func TestReadDigest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "containerimage.digest preferred",
			body: `{"containerimage.digest":"sha256:aaaa","image.digest":"sha256:bbbb"}`,
			want: "sha256:aaaa",
		},
		{
			name: "fallback to image.digest",
			body: `{"image.digest":"sha256:cccc"}`,
			want: "sha256:cccc",
		},
		{
			name:    "no digest fields",
			body:    `{"otherkey":"value"}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			body:    `{not json`,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metadata.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := readDigest(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (got %q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadDigest_MissingFile guards the case where buildctl never
// produced the metadata file — typically a build that crashed before
// the writeMetadata stage, in which case we should propagate the FS
// error and let the caller surface it instead of returning an empty
// digest silently.
func TestReadDigest_MissingFile(t *testing.T) {
	_, err := readDigest("/nope/metadata.json")
	if err == nil {
		t.Fatalf("expected error reading missing file")
	}
}
