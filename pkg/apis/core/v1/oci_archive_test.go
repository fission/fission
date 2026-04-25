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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
	corev1 "k8s.io/api/core/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

func TestOCIArchiveValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		oci     OCIArchive
		wantErr bool
	}{
		{
			name:    "valid tag reference",
			oci:     OCIArchive{Image: "ghcr.io/example/hello:v1"},
			wantErr: false,
		},
		{
			name:    "valid digest reference",
			oci:     OCIArchive{Image: "ghcr.io/example/hello@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
			wantErr: false,
		},
		{
			name:    "valid with pinned digest field",
			oci:     OCIArchive{Image: "ghcr.io/example/hello:v1", Digest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
			wantErr: false,
		},
		{
			name:    "valid with subpath",
			oci:     OCIArchive{Image: "ghcr.io/example/hello:v1", SubPath: "deploy"},
			wantErr: false,
		},
		{
			name: "valid with image pull secrets",
			oci: OCIArchive{
				Image:            "ghcr.io/example/hello:v1",
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: "regcred"}},
			},
			wantErr: false,
		},
		{
			name:    "empty image",
			oci:     OCIArchive{},
			wantErr: true,
		},
		{
			name:    "whitespace-only image",
			oci:     OCIArchive{Image: "   "},
			wantErr: true,
		},
		{
			name:    "image contains whitespace",
			oci:     OCIArchive{Image: "ghcr.io/example/hello :v1"},
			wantErr: true,
		},
		{
			name:    "image without repository separator",
			oci:     OCIArchive{Image: "invalidimage"},
			wantErr: true,
		},
		{
			name:    "digest missing algorithm prefix",
			oci:     OCIArchive{Image: "ghcr.io/example/hello:v1", Digest: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
			wantErr: true,
		},
		{
			name:    "digest with wrong algorithm",
			oci:     OCIArchive{Image: "ghcr.io/example/hello:v1", Digest: "md5:deadbeef"},
			wantErr: true,
		},
		{
			name:    "digest with uppercase hex",
			oci:     OCIArchive{Image: "ghcr.io/example/hello:v1", Digest: "sha256:E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855"},
			wantErr: true,
		},
		{
			name:    "digest too short",
			oci:     OCIArchive{Image: "ghcr.io/example/hello:v1", Digest: "sha256:abcdef"},
			wantErr: true,
		},
		{
			name: "image pull secret with empty name",
			oci: OCIArchive{
				Image:            "ghcr.io/example/hello:v1",
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: ""}},
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.oci.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Snapshot the stringified error so future regressions in
			// the error message surface are caught.
			snaps.MatchSnapshot(t, fmt.Sprint(err))
		})
	}
}

func TestArchiveValidateWithOCI(t *testing.T) {
	validOCI := &OCIArchive{Image: "ghcr.io/example/hello:v1"}

	for _, tc := range []struct {
		name    string
		archive Archive
		wantErr bool
	}{
		{
			name:    "empty archive is valid",
			archive: Archive{},
			wantErr: false,
		},
		{
			name:    "literal only",
			archive: Archive{Type: ArchiveTypeLiteral, Literal: []byte("code")},
			wantErr: false,
		},
		{
			name:    "url only",
			archive: Archive{Type: ArchiveTypeUrl, URL: "https://example.com/pkg.zip"},
			wantErr: false,
		},
		{
			name:    "oci only",
			archive: Archive{Type: ArchiveTypeOCI, OCI: validOCI},
			wantErr: false,
		},
		{
			name:    "oci without explicit type is still valid",
			archive: Archive{OCI: validOCI},
			wantErr: false,
		},
		{
			name:    "oci and url are mutually exclusive",
			archive: Archive{OCI: validOCI, URL: "https://example.com/pkg.zip"},
			wantErr: true,
		},
		{
			name:    "oci and literal are mutually exclusive",
			archive: Archive{OCI: validOCI, Literal: []byte("code")},
			wantErr: true,
		},
		{
			name:    "url and literal are mutually exclusive",
			archive: Archive{URL: "https://example.com/pkg.zip", Literal: []byte("code")},
			wantErr: true,
		},
		{
			name: "all three set is invalid",
			archive: Archive{
				OCI:     validOCI,
				URL:     "https://example.com/pkg.zip",
				Literal: []byte("code"),
			},
			wantErr: true,
		},
		{
			name:    "unknown archive type is invalid",
			archive: Archive{Type: ArchiveType("chocolate")},
			wantErr: true,
		},
		{
			name:    "invalid oci payload surfaces nested error",
			archive: Archive{OCI: &OCIArchive{Image: ""}},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.archive.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			snaps.MatchSnapshot(t, fmt.Sprint(err))
		})
	}
}

func TestPackageSpecValidateWithOCI(t *testing.T) {
	env := EnvironmentReference{Namespace: "fission", Name: "node"}
	ociDeploy := Archive{OCI: &OCIArchive{Image: "ghcr.io/example/hello:v1"}}

	for _, tc := range []struct {
		name    string
		spec    PackageSpec
		wantErr bool
	}{
		{
			name:    "oci deployment with no source is valid",
			spec:    PackageSpec{Environment: env, Deployment: ociDeploy},
			wantErr: false,
		},
		{
			name: "oci source + oci deployment is valid",
			spec: PackageSpec{
				Environment: env,
				Source:      Archive{OCI: &OCIArchive{Image: "ghcr.io/example/hello-src:v1"}},
				Deployment:  ociDeploy,
			},
			wantErr: false,
		},
		{
			name: "oci source with invalid image is rejected",
			spec: PackageSpec{
				Environment: env,
				Source:      Archive{OCI: &OCIArchive{Image: ""}},
				Deployment:  ociDeploy,
			},
			wantErr: true,
		},
		{
			name: "oci + url in same archive surfaces mutual-exclusion error",
			spec: PackageSpec{
				Environment: env,
				Deployment: Archive{
					OCI: &OCIArchive{Image: "ghcr.io/example/hello:v1"},
					URL: "https://example.com/pkg.zip",
				},
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestOCIArchiveDeepCopyIsIndependent(t *testing.T) {
	orig := &OCIArchive{
		Image:            "ghcr.io/example/hello:v1",
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "regcred"}},
		SubPath:          "deploy",
		Digest:           "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}

	cp := orig.DeepCopy()
	if !reflect.DeepEqual(orig, cp) {
		t.Fatalf("deepcopy differs from original:\n  orig=%+v\n  cp=  %+v", orig, cp)
	}

	// Mutate the copy and verify the original is untouched.
	cp.Image = "ghcr.io/example/other:v2"
	cp.ImagePullSecrets[0].Name = "different"
	if orig.Image != "ghcr.io/example/hello:v1" {
		t.Fatalf("original.Image mutated through copy: %q", orig.Image)
	}
	if orig.ImagePullSecrets[0].Name != "regcred" {
		t.Fatalf("original.ImagePullSecrets mutated through copy: %q", orig.ImagePullSecrets[0].Name)
	}
}

func TestArchiveWithOCIJSONRoundTrip(t *testing.T) {
	orig := Archive{
		Type: ArchiveTypeOCI,
		OCI: &OCIArchive{
			Image:            "ghcr.io/example/hello:v1",
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "regcred"}},
			SubPath:          "deploy",
			Digest:           "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Archive
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(orig, got) {
		t.Fatalf("round-trip lost fidelity:\n  want=%+v\n  got= %+v", orig, got)
	}

	// Also snapshot the marshaled form so downstream consumers (CLI,
	// webhook) can rely on a stable on-wire shape.
	snaps.MatchSnapshot(t, string(data))
}

func TestArchiveWithoutOCIOmitsField(t *testing.T) {
	// Regression guard: an Archive without OCI must not emit an `oci`
	// key in its JSON form — otherwise we'd change the wire shape of
	// every existing Package.
	a := Archive{Type: ArchiveTypeLiteral, Literal: []byte("code")}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"oci"`) {
		t.Fatalf("expected no oci key, got %s", data)
	}
}

func TestPackageCRDSchemaSnapshot(t *testing.T) {
	// Locate the generated CRD YAML relative to this file.
	// pkg/apis/core/v1 -> repo root is four dirs up.
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	crdPath := filepath.Join(repoRoot, "crds", "v1", "fission.io_packages.yaml")
	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("read %s: %v", crdPath, err)
	}

	var crd map[string]any
	if err := sigsyaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("parse CRD yaml: %v", err)
	}

	// Navigate to spec.versions[0].schema.openAPIV3Schema.properties.spec
	specProps, err := dig[map[string]any](
		crd,
		"spec", "versions", 0, "schema", "openAPIV3Schema", "properties", "spec", "properties",
	)
	if err != nil {
		t.Fatalf("navigate CRD: %v", err)
	}

	// Snapshot the source and deployment Archive subschemas — these
	// are what CRD consumers (kubectl, Argo CD, operators) rely on.
	// Capturing both guarantees the OCI field was wired symmetrically.
	toSnapshot := map[string]any{}
	for _, key := range []string{"source", "deployment"} {
		v, ok := specProps[key]
		if !ok {
			t.Fatalf("%s Archive schema missing from CRD", key)
		}
		toSnapshot[key] = v
	}

	out, err := sigsyaml.Marshal(toSnapshot)
	if err != nil {
		t.Fatalf("marshal subschema: %v", err)
	}

	snaps.MatchSnapshot(t, string(out))
}

// dig walks a JSON/YAML-shaped map[string]any or []any using a path of
// string keys and int indices, asserting the leaf is of type T.
func dig[T any](root any, path ...any) (T, error) {
	var zero T
	cur := root
	for i, p := range path {
		switch key := p.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return zero, fmt.Errorf("path[%d] %q: expected map, got %T", i, key, cur)
			}
			cur, ok = m[key]
			if !ok {
				return zero, fmt.Errorf("path[%d] %q: key not found", i, key)
			}
		case int:
			s, ok := cur.([]any)
			if !ok {
				return zero, fmt.Errorf("path[%d] %d: expected slice, got %T", i, key, cur)
			}
			if key < 0 || key >= len(s) {
				return zero, fmt.Errorf("path[%d] %d: index out of range (len=%d)", i, key, len(s))
			}
			cur = s[key]
		default:
			return zero, fmt.Errorf("path[%d]: unsupported key type %T", i, p)
		}
	}
	out, ok := cur.(T)
	if !ok {
		return zero, fmt.Errorf("leaf type mismatch: got %T, want %T", cur, zero)
	}
	return out, nil
}
