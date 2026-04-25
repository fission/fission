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

package util

import (
	"reflect"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func TestMergeImagePullSecrets(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		oci  []apiv1.LocalObjectReference
		want []apiv1.LocalObjectReference
	}{
		{name: "both empty", env: "", oci: nil, want: nil},
		{
			name: "env only",
			env:  "envps",
			oci:  nil,
			want: []apiv1.LocalObjectReference{{Name: "envps"}},
		},
		{
			name: "oci only",
			env:  "",
			oci:  []apiv1.LocalObjectReference{{Name: "ociA"}, {Name: "ociB"}},
			want: []apiv1.LocalObjectReference{{Name: "ociA"}, {Name: "ociB"}},
		},
		{
			name: "both unique",
			env:  "envps",
			oci:  []apiv1.LocalObjectReference{{Name: "ociA"}},
			want: []apiv1.LocalObjectReference{{Name: "envps"}, {Name: "ociA"}},
		},
		{
			name: "duplicate dropped",
			env:  "shared",
			oci:  []apiv1.LocalObjectReference{{Name: "shared"}, {Name: "extra"}},
			want: []apiv1.LocalObjectReference{{Name: "shared"}, {Name: "extra"}},
		},
		{
			name: "oci internal duplicates collapsed",
			env:  "",
			oci:  []apiv1.LocalObjectReference{{Name: "a"}, {Name: "a"}, {Name: "b"}},
			want: []apiv1.LocalObjectReference{{Name: "a"}, {Name: "b"}},
		},
		{
			name: "empty names skipped",
			env:  "",
			oci:  []apiv1.LocalObjectReference{{Name: ""}, {Name: "real"}},
			want: []apiv1.LocalObjectReference{{Name: "real"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeImagePullSecrets(tc.env, tc.oci)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("MergeImagePullSecrets(%q, %+v) = %+v, want %+v", tc.env, tc.oci, got, tc.want)
			}
		})
	}
}

func TestApplyOCIImagePullSecrets(t *testing.T) {
	t.Run("both empty leaves podspec untouched", func(t *testing.T) {
		spec := apiv1.PodSpec{ImagePullSecrets: []apiv1.LocalObjectReference{{Name: "preexisting"}}}
		got := ApplyOCIImagePullSecrets("", nil, spec)
		want := []apiv1.LocalObjectReference{{Name: "preexisting"}}
		if !reflect.DeepEqual(got.ImagePullSecrets, want) {
			t.Fatalf("got %+v, want %+v", got.ImagePullSecrets, want)
		}
	})
	t.Run("oci secrets overwrite", func(t *testing.T) {
		spec := apiv1.PodSpec{ImagePullSecrets: []apiv1.LocalObjectReference{{Name: "preexisting"}}}
		got := ApplyOCIImagePullSecrets("", []apiv1.LocalObjectReference{{Name: "ociA"}}, spec)
		want := []apiv1.LocalObjectReference{{Name: "ociA"}}
		if !reflect.DeepEqual(got.ImagePullSecrets, want) {
			t.Fatalf("got %+v, want %+v", got.ImagePullSecrets, want)
		}
	})
	t.Run("env + oci merged onto empty podspec", func(t *testing.T) {
		got := ApplyOCIImagePullSecrets("env", []apiv1.LocalObjectReference{{Name: "ociA"}}, apiv1.PodSpec{})
		want := []apiv1.LocalObjectReference{{Name: "env"}, {Name: "ociA"}}
		if !reflect.DeepEqual(got.ImagePullSecrets, want) {
			t.Fatalf("got %+v, want %+v", got.ImagePullSecrets, want)
		}
	})
}

func TestGetFunctionOCIArchive(t *testing.T) {
	const (
		ns      = "default"
		pkgName = "hello-pkg"
	)
	makeFn := func(refName string) *fv1.Function {
		return &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: ns},
			Spec: fv1.FunctionSpec{
				Package: fv1.FunctionPackageRef{
					PackageRef: fv1.PackageRef{Name: refName, Namespace: ns},
				},
			},
		}
	}

	t.Run("function with no package ref returns nil", func(t *testing.T) {
		client := fClient.NewSimpleClientset() // nolint:staticcheck
		got, err := GetFunctionOCIArchive(t.Context(), client, makeFn(""))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil OCIArchive, got %+v", got)
		}
	})

	t.Run("missing package returns nil without error", func(t *testing.T) {
		client := fClient.NewSimpleClientset() // nolint:staticcheck
		got, err := GetFunctionOCIArchive(t.Context(), client, makeFn(pkgName))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil OCIArchive, got %+v", got)
		}
	})

	t.Run("package without OCI returns nil", func(t *testing.T) {
		client := fClient.NewSimpleClientset(&fv1.Package{ // nolint:staticcheck
			ObjectMeta: metav1.ObjectMeta{Name: pkgName, Namespace: ns},
			Spec: fv1.PackageSpec{
				Deployment: fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "http://example/pkg.zip"},
			},
		})
		got, err := GetFunctionOCIArchive(t.Context(), client, makeFn(pkgName))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil OCIArchive, got %+v", got)
		}
	})

	t.Run("package with OCI returns archive", func(t *testing.T) {
		oci := &fv1.OCIArchive{
			Image: "ghcr.io/example/hello:v1",
			ImagePullSecrets: []apiv1.LocalObjectReference{
				{Name: "ghcr-pull"},
			},
		}
		client := fClient.NewSimpleClientset(&fv1.Package{ // nolint:staticcheck
			ObjectMeta: metav1.ObjectMeta{Name: pkgName, Namespace: ns},
			Spec:       fv1.PackageSpec{Deployment: fv1.Archive{Type: fv1.ArchiveTypeOCI, OCI: oci}},
		})
		got, err := GetFunctionOCIArchive(t.Context(), client, makeFn(pkgName))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(got, oci) {
			t.Fatalf("got %+v, want %+v", got, oci)
		}
	})
}
