// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"strings"
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

// pyHello returns a single-file python deployment whose main() returns body —
// the same layout buildHelloZip produces, so anything that serves via
// --deploy serves via OCI.
func pyHello(body string) map[string]string {
	return map[string]string{"hello.py": "def main():\n    return \"" + body + "\""}
}

// TestOCIPackagePoolmgr covers RFC-0001 Path A end-to-end on the default
// poolmgr executor: the test pushes a code image to the in-cluster registry,
// creates an OCI package + function + route, and the fetcher pulls and
// extracts the image at specialization time.
func TestOCIPackagePoolmgr(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, inclusterAddr := framework.RequireRegistry(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-oci-" + ns.ID
	pkgName := "oci-pm-pkg-" + ns.ID
	fnName := "fn-oci-pm-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	ref, _ := framework.PushCodeImage(t, hostAddr, inclusterAddr,
		"fission-test/hello-"+ns.ID, "v1", pyHello("Hello, OCI!"))

	ns.CreatePackage(t, ctx, framework.PackageOptions{Name: pkgName, Env: envName, OCI: ref})
	ns.WaitForPackageBuildStatus(t, ctx, pkgName, fv1.BuildStatusNone, time.Minute)

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("Hello, OCI!"))
	assert.Contains(t, body, "Hello, OCI!")
}

// TestOCIPackagePoolmgrDigestMismatch proves the digest pin is enforced on
// the pull path: a package pinning the wrong digest must never serve — the
// fetcher refuses the image and specialization fails.
func TestOCIPackagePoolmgrDigestMismatch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	hostAddr, inclusterAddr := framework.RequireRegistry(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-ocibad-" + ns.ID
	pkgName := "oci-bad-pkg-" + ns.ID
	fnName := "fn-oci-bad-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
	})

	ref, digest := framework.PushCodeImage(t, hostAddr, inclusterAddr,
		"fission-test/bad-digest-"+ns.ID, "v1", pyHello("never served"))
	wrong := "sha256:" + strings.Repeat("0", 64)
	require.NotEqual(t, digest, wrong)

	// The digest field has no CLI flag; create the Package CR directly.
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: pkgName, Namespace: ns.Name},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Namespace: ns.Name, Name: envName},
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				OCI:  &fv1.OCIArchive{Image: ref, Digest: wrong},
			},
		},
	}
	_, err := f.FissionClient().CoreV1().Packages(ns.Name).Create(ctx, pkg, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = f.FissionClient().CoreV1().Packages(ns.Name).Delete(context.Background(), pkgName, metav1.DeleteOptions{})
	})

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	// The invocation must surface a 5xx (specialization failure), never the
	// function body.
	f.Router(t).GetEventually(t, ctx, "/"+fnName, func(status int, body string) bool {
		require.NotContains(t, body, "never served", "wrong-digest image must not serve")
		return status >= 500
	})
}
