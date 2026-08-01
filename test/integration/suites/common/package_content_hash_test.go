// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// helloPySource returns a single-file python function that echoes marker, so a
// content change is observable in the response body.
func helloPySource(marker string) string {
	return "def main():\n    return \"" + marker + "\", 200\n"
}

// writeHelloPy materializes helloPySource under t.TempDir for `fn create --code`.
func writeHelloPy(t *testing.T, marker string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "hello.py")
	require.NoError(t, os.WriteFile(dst, []byte(helloPySource(marker)), 0o644))
	return dst
}

// TestPackageContentHashRestampsWithoutCLI is the RFC-0029 §3 golden path, and
// the case that had NO propagation before this feature: a deploy-only package
// never builds, so the build-success re-stamp that recycles referencing
// Functions' pods never ran for it.
//
// The package spec is edited through the typed clientset — deliberately NOT
// through `fission fn update`, because the CLI stamps the referencing Functions
// itself. Editing the CRD directly is what Argo/Flux do, and it is the only way
// to observe whether the controller propagates on its own.
func TestPackageContentHashRestampsWithoutCLI(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "py-pkghash-" + ns.ID
	fnName := "fn-pkghash-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: writeHelloPy(t, "content-v1"),
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 2,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("content-v1"))
	require.Contains(t, body, "content-v1")

	fc := f.FissionClient().CoreV1()
	fn, err := fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoError(t, err)
	pkgName := fn.Spec.Package.PackageRef.Name
	stampBefore := fn.Spec.Package.PackageRef.ResourceVersion

	// The controller must have recorded a hash for the package as it stands —
	// otherwise the first content change would be swallowed as a seed.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pkg, err := fc.Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		assert.NotEmpty(c, pkg.Status.ContentHash, "package must carry a recorded content hash before a change")
	}, 2*time.Minute, 2*time.Second)

	// Change the delivered content the way a GitOps controller would: edit the
	// Package spec directly, with no CLI and no Function edit.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pkg, err := fc.Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		// Replace the archive wholesale rather than assigning Literal over
		// whatever the CLI uploaded. At most one of literal/url/oci may be
		// set, so leaving a URL behind would have the webhook reject this
		// update — which today only stays hidden because a `--code` archive
		// this small is inlined as a literal.
		pkg.Spec.Deployment = fv1.Archive{
			Type:    fv1.ArchiveTypeLiteral,
			Literal: []byte(helloPySource("content-v2")),
		}
		_, err = fc.Packages(ns.Name).Update(ctx, pkg, metav1.UpdateOptions{})
		assert.NoError(c, err)
	}, time.Minute, 2*time.Second)

	// The re-stamp is what recycles the pods, so assert on it directly as well
	// as on the served response — the stamp is the mechanism, the body is the
	// user-visible outcome.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got, err := fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		assert.NotEqual(c, stampBefore, got.Spec.Package.PackageRef.ResourceVersion,
			"a content change applied without the CLI must re-stamp referencing functions")
	}, 3*time.Minute, 3*time.Second)

	body = f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("content-v2"))
	assert.Contains(t, body, "content-v2", "the function must serve the new content after the re-stamp")
}

// TestPackageContentHashUnchangedIsNoOp is RFC-0029 §3's no-op invariant: an
// unchanged re-apply must provably do nothing. A GitOps controller re-applies
// on every sync, so a reconciler that treated re-application as a change would
// rebuild and recycle pods continuously.
func TestPackageContentHashUnchangedIsNoOp(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "py-pkgnoop-" + ns.ID
	fnName := "fn-pkgnoop-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: writeHelloPy(t, "noop-v1"),
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 2,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	require.Contains(t, f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("noop-v1")), "noop-v1")

	fc := f.FissionClient().CoreV1()
	fn, err := fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoError(t, err)
	pkgName := fn.Spec.Package.PackageRef.Name

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pkg, err := fc.Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		assert.NotEmpty(c, pkg.Status.ContentHash)
	}, 2*time.Minute, 2*time.Second)

	fn, err = fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoError(t, err)
	stampBefore := fn.Spec.Package.PackageRef.ResourceVersion

	// Re-apply the package with its content untouched, three times — the shape
	// of a repeated GitOps sync. Only a label moves, so Generation changes
	// while the delivered bytes do not.
	for i := range 3 {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			pkg, err := fc.Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
			if !assert.NoError(c, err) {
				return
			}
			if pkg.Labels == nil {
				pkg.Labels = map[string]string{}
			}
			pkg.Labels["resync"] = string(rune('a' + i))
			_, err = fc.Packages(ns.Name).Update(ctx, pkg, metav1.UpdateOptions{})
			assert.NoError(c, err)
		}, time.Minute, 2*time.Second)
	}

	// Bounded negative assertion: poll for a window and fail the moment the
	// stamp moves, rather than sampling once at the end.
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		got, err := fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, stampBefore, got.Spec.Package.PackageRef.ResourceVersion,
			"re-applying unchanged package content must not re-stamp referencing functions")
		time.Sleep(3 * time.Second)
	}

	assert.Contains(t, f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("noop-v1")), "noop-v1",
		"the function must still be serving after repeated no-op re-applies")
}
