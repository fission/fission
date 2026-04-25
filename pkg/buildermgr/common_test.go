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

package buildermgr

import (
	"reflect"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fetcher"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func newPkg(t *testing.T) *fv1.Package {
	t.Helper()
	return &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pkg-x",
			Namespace: "default",
		},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{
				Name:      "node",
				Namespace: "default",
			},
		},
	}
}

func TestUpdatePackage_TarballOutcome(t *testing.T) {
	pkg := newPkg(t)
	client := fClient.NewSimpleClientset(pkg) // nolint:staticcheck

	outcome := &buildOutcome{
		TarballUpload: &fetcher.ArchiveUploadResponse{
			ArchiveDownloadUrl: "http://storagesvc/archive/abc",
			Checksum:           fv1.Checksum{Type: fv1.ChecksumTypeSHA256, Sum: "deadbeef"},
		},
	}
	updated, err := updatePackage(t.Context(), loggerfactory.GetLogger(), client, pkg,
		fv1.BuildStatusSucceeded, "build ok", outcome)
	if err != nil {
		t.Fatalf("updatePackage: %v", err)
	}
	if updated.Status.BuildStatus != fv1.BuildStatusSucceeded {
		t.Fatalf("BuildStatus = %q, want succeeded", updated.Status.BuildStatus)
	}
	if updated.Spec.Deployment.Type != fv1.ArchiveTypeUrl {
		t.Fatalf("archive type = %q, want %q", updated.Spec.Deployment.Type, fv1.ArchiveTypeUrl)
	}
	if updated.Spec.Deployment.URL != "http://storagesvc/archive/abc" {
		t.Fatalf("URL = %q, want stored URL", updated.Spec.Deployment.URL)
	}
	if updated.Spec.Deployment.OCI != nil {
		t.Fatalf("OCI must be nil for tarball outcome, got %+v", updated.Spec.Deployment.OCI)
	}
}

func TestUpdatePackage_OCIOutcome(t *testing.T) {
	pkg := newPkg(t)
	client := fClient.NewSimpleClientset(pkg) // nolint:staticcheck

	outcome := &buildOutcome{
		OCI: &fv1.OCIArchive{
			Image:  "ghcr.io/myorg/pkg-x:abc",
			Digest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			ImagePullSecrets: []apiv1.LocalObjectReference{
				{Name: "ghcr-pull"},
			},
		},
	}
	updated, err := updatePackage(t.Context(), loggerfactory.GetLogger(), client, pkg,
		fv1.BuildStatusSucceeded, "buildkit ok", outcome)
	if err != nil {
		t.Fatalf("updatePackage: %v", err)
	}
	if updated.Spec.Deployment.Type != fv1.ArchiveTypeOCI {
		t.Fatalf("archive type = %q, want %q", updated.Spec.Deployment.Type, fv1.ArchiveTypeOCI)
	}
	if updated.Spec.Deployment.URL != "" {
		t.Fatalf("URL must be empty for OCI outcome, got %q", updated.Spec.Deployment.URL)
	}
	if !reflect.DeepEqual(updated.Spec.Deployment.OCI, outcome.OCI) {
		t.Fatalf("OCI = %+v, want %+v", updated.Spec.Deployment.OCI, outcome.OCI)
	}
}

func TestUpdatePackage_NilOutcomeLeavesDeployment(t *testing.T) {
	pkg := newPkg(t)
	pkg.Spec.Deployment = fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "preexisting"}
	client := fClient.NewSimpleClientset(pkg) // nolint:staticcheck

	updated, err := updatePackage(t.Context(), loggerfactory.GetLogger(), client, pkg,
		fv1.BuildStatusFailed, "boom", nil)
	if err != nil {
		t.Fatalf("updatePackage: %v", err)
	}
	if updated.Status.BuildStatus != fv1.BuildStatusFailed {
		t.Fatalf("BuildStatus = %q, want failed", updated.Status.BuildStatus)
	}
	if updated.Spec.Deployment.URL != "preexisting" {
		t.Fatalf("preexisting deployment URL must be untouched on failure, got %q", updated.Spec.Deployment.URL)
	}
}
