// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestOCIPackageReconciles covers RFC-0001 Phase 1: an OCI package is a
// first-class Package CR that reconciles to BuildStatusNone (nothing to
// build) without touching any registry — the image reference deliberately
// does not exist because no data-path code runs for a package that no
// function invokes.
func TestOCIPackageReconciles(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-oci-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	pkgName := "oci-pkg-" + ns.ID
	const imageRef = "registry.invalid/example/hello-code:v1"
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkgName, Env: envName, OCI: imageRef,
	})

	pkg := ns.GetPackage(t, ctx, pkgName)
	assert.Equal(t, fv1.ArchiveTypeOCI, pkg.Spec.Deployment.Type)
	require.NotNil(t, pkg.Spec.Deployment.OCI)
	assert.Equal(t, imageRef, pkg.Spec.Deployment.OCI.Image)
	assert.Empty(t, pkg.Spec.Deployment.URL)
	assert.Empty(t, pkg.Spec.Deployment.Literal)
	assert.True(t, pkg.Spec.Source.IsEmpty(), "source archive must stay empty")

	// The buildermgr derives the initial status from the spec archives: an
	// OCI deployment archive means nothing to build.
	ns.WaitForPackageBuildStatus(t, ctx, pkgName, fv1.BuildStatusNone, 2*time.Minute)
	pkg = ns.GetPackage(t, ctx, pkgName)
	assert.Empty(t, pkg.Status.BuildLog, "no builder must have run for an OCI package")
}

// TestOCIPackageCELMutualExclusion proves the API server itself (CEL on the
// Archive schema) rejects a Package whose deployment archive sets both url
// and oci — defense in depth ahead of the webhook and CLI. CEL cannot cover
// combinations involving the byte-format literal field (see types.go); those
// are rejected by the webhook with the same message.
func TestOCIPackageCELMutualExclusion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "oci-cel-" + ns.ID, Namespace: ns.Name},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Namespace: ns.Name, Name: "node"},
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				URL:  "http://example.com/deploy.zip",
				OCI:  &fv1.OCIArchive{Image: "registry.invalid/example/hello-code:v1"},
			},
		},
	}
	_, err := f.FissionClient().CoreV1().Packages(ns.Name).Create(ctx, pkg, metav1.CreateOptions{})
	require.Error(t, err, "API server must reject url+oci on one archive")
	assert.Contains(t, err.Error(), "at most one of literal, url, or oci")
}
