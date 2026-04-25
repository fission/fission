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

package _package

import (
	"reflect"
	"strings"
	"testing"

	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func TestBuildOCIArchive_NotSet(t *testing.T) {
	in := dummy.TestFlagSet()
	if got := BuildOCIArchive(in); got != nil {
		t.Fatalf("expected nil when --oci absent, got %+v", got)
	}
}

func TestBuildOCIArchive_FullPopulation(t *testing.T) {
	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgOCI, "ghcr.io/example/hello:v1")
	in.Set(flagkey.PkgOCISubPath, "deploy")
	in.Set(flagkey.PkgOCIDigest, "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	in.Set(flagkey.PkgOCIPullSecret, []string{"ghcr-pull", "", "extra"})

	got := BuildOCIArchive(in)
	want := &fv1.OCIArchive{
		Image:   "ghcr.io/example/hello:v1",
		SubPath: "deploy",
		Digest:  "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ImagePullSecrets: []apiv1.LocalObjectReference{
			{Name: "ghcr-pull"},
			{Name: "extra"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestValidateOCIMutualExclusion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flags     map[string]any
		wantError bool
		errSub    string
	}{
		{name: "no oci is fine", flags: map[string]any{}, wantError: false},
		{
			name: "oci alone is fine",
			flags: map[string]any{
				flagkey.PkgOCI: "ghcr.io/example/hello:v1",
			},
			wantError: false,
		},
		{
			name: "oci + code rejected",
			flags: map[string]any{
				flagkey.PkgOCI:  "ghcr.io/example/hello:v1",
				flagkey.PkgCode: "main.js",
			},
			wantError: true,
			errSub:    flagkey.PkgCode,
		},
		{
			name: "oci + src rejected",
			flags: map[string]any{
				flagkey.PkgOCI:        "ghcr.io/example/hello:v1",
				flagkey.PkgSrcArchive: []string{"src.zip"},
			},
			wantError: true,
			errSub:    flagkey.PkgSrcArchive,
		},
		{
			name: "oci + deploy rejected",
			flags: map[string]any{
				flagkey.PkgOCI:           "ghcr.io/example/hello:v1",
				flagkey.PkgDeployArchive: []string{"deploy.zip"},
			},
			wantError: true,
			errSub:    flagkey.PkgDeployArchive,
		},
		{
			name: "oci + buildcmd rejected",
			flags: map[string]any{
				flagkey.PkgOCI:      "ghcr.io/example/hello:v1",
				flagkey.PkgBuildCmd: "build.sh",
			},
			wantError: true,
			errSub:    flagkey.PkgBuildCmd,
		},
		{
			name: "oci + checksum rejected",
			flags: map[string]any{
				flagkey.PkgOCI:            "ghcr.io/example/hello:v1",
				flagkey.PkgDeployChecksum: "sha256:abc",
			},
			wantError: true,
			errSub:    flagkey.PkgDeployChecksum,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := dummy.TestFlagSet()
			for k, v := range tc.flags {
				in.Set(k, v)
			}
			err := ValidateOCIMutualExclusion(in)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q must mention flag %q", err.Error(), tc.errSub)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
