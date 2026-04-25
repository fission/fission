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

package v1

import (
	"strings"
	"testing"
)

func TestBuilderValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		builder Builder
		wantErr bool
		errSub  string
	}{
		{name: "empty kind ok", builder: Builder{Image: "img"}, wantErr: false},
		{name: "tarball kind ok", builder: Builder{Image: "img", Kind: BuilderKindTarball}, wantErr: false},
		{
			name:    "buildkit without registry rejected",
			builder: Builder{Image: "img", Kind: BuilderKindBuildKit},
			wantErr: true,
			errSub:  "registry URL",
		},
		{
			name:    "buildkit with empty registry url rejected",
			builder: Builder{Image: "img", Kind: BuilderKindBuildKit, Registry: &BuilderRegistry{}},
			wantErr: true,
			errSub:  "registry URL",
		},
		{
			name:    "buildkit with registry ok",
			builder: Builder{Image: "img", Kind: BuilderKindBuildKit, Registry: &BuilderRegistry{URL: "ghcr.io/myorg/fns"}},
			wantErr: false,
		},
		{
			name:    "registry url with whitespace rejected",
			builder: Builder{Kind: BuilderKindBuildKit, Registry: &BuilderRegistry{URL: "ghcr.io /space"}},
			wantErr: true,
			errSub:  "whitespace",
		},
		{
			name:    "unknown kind rejected",
			builder: Builder{Kind: "podman"},
			wantErr: true,
			errSub:  "Builder.Kind",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.builder.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q must mention %q", err.Error(), tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
