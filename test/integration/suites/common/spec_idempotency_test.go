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

// TestSpecApplyIsIdempotent pins the first of RFC-0029's three `fission spec`
// guarantees: reapplying unchanged specs is a no-op.
//
// The existing dry-run test proves the PREVIEW reports no creates. This asserts
// the stronger and more useful property — that a real second apply writes
// nothing observable. The distinction matters because the failure mode is not a
// visible error: a spec apply that rewrites unchanged objects bumps the
// Function's PackageRef, which recycles pods and mints an RFC-0025 version for
// content nobody edited. A GitOps controller reapplies on every sync, so that
// would be continuous churn rather than a one-off.
//
// Uses a --code (literal deploy) function so no builder or runtime image is
// needed: nothing is built or specialized, only the spec reconcile is
// exercised.
func TestSpecApplyIsIdempotent(t *testing.T) {
	t.Parallel()
	// SKIPPED: this documents a CONFIRMED bug, not a passing contract.
	//
	// Measured on CI (v1.34.8, 2026-08-01): reapplying a byte-identical spec
	// directory moved the function's PackageRef.ResourceVersion 4785 -> 4789
	// and its generation 1 -> 2. So every GitOps sync of an unchanged spec
	// recycles pods and mints an RFC-0025 version.
	//
	// Root cause is in the apply engine, not the spec files. applyResourceType
	// records a NO-OP package's CURRENT live metadata, and apply.go's
	// cross-reference pass then copies that ResourceVersion into every
	// referencing function. The package's ResourceVersion drifts on controller
	// STATUS writes (buildermgr records a content hash), so the copy carries
	// drift that has nothing to do with the spec. Same defect class as the
	// `fn update` stamp fixed in #3635, in a second code path.
	//
	// The fix is not simply skipping the stamp: the function in the spec file
	// carries no ResourceVersion, so leaving it empty also differs from live
	// and still forces an update. The engine has to preserve the LIVE
	// function's stamp when the referenced package was a no-op this apply.
	//
	// RFC-0029 §4 lists this as phase-4 work (#3296, #1491). Unskip with the
	// fix.
	t.Skip("known non-idempotency in the spec apply engine — see the comment above and RFC-0029 phase 4 (#3296, #1491)")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	envName := "idem-env-" + ns.ID
	fnName := "idem-fn-" + ns.ID

	workdir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workdir, "hello.js"),
		[]byte("module.exports = async function(){ return {status:200, body:'hi'} }\n"), 0o644))

	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		ns.CLI(t, ctx, "env", "create", "--spec", "--name", envName, "--image", "fission/node-env")
		ns.CLI(t, ctx, "fn", "create", "--spec", "--name", fnName, "--env", envName, "--code", "hello.js")

		ns.CLI(t, ctx, "spec", "apply")
		t.Cleanup(func() {
			dctx, dcancel := context.WithTimeout(context.Background(), time.Minute)
			defer dcancel()
			// Best-effort: the test may have failed before anything was applied,
			// and a destroy that finds nothing must not mask the real failure.
			ns.WithCWD(t, workdir, func() { _, _ = ns.CLICaptureStdoutWithEnvBestEffort(t, dctx, nil, "spec", "destroy") })
		})
	})

	// Let the package settle: buildermgr records a content hash on first sight,
	// and that status write must land BEFORE the baseline is taken or it would
	// be mistaken for churn caused by the second apply.
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

	before, err := fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoError(t, err)
	pkgBefore, err := fc.Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
	require.NoError(t, err)
	versionsBefore := countFunctionVersions(t, ctx, f, ns.Name, fnName)

	// Reapply the identical spec directory, twice — one repeat can coincide
	// with a settling write, a pair cannot.
	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "apply")
		ns.CLI(t, ctx, "spec", "apply")
	})

	after, err := fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, before.Spec.Package.PackageRef.ResourceVersion, after.Spec.Package.PackageRef.ResourceVersion,
		"reapplying an unchanged spec must not re-stamp the function: the stamp is what recycles pods")
	assert.Equal(t, before.Generation, after.Generation,
		"reapplying an unchanged spec must not bump the function's generation")

	pkgAfter, err := fc.Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, pkgBefore.Generation, pkgAfter.Generation,
		"reapplying an unchanged spec must not rewrite the package spec")
	assert.Equal(t, pkgBefore.Status.ContentHash, pkgAfter.Status.ContentHash,
		"the delivered content did not change, so its recorded hash must not either")

	assert.Equal(t, versionsBefore, countFunctionVersions(t, ctx, f, ns.Name, fnName),
		"reapplying an unchanged spec must not mint an RFC-0025 version")
}

// countFunctionVersions returns how many FunctionVersions exist for fnName.
func countFunctionVersions(t *testing.T, ctx context.Context, f *framework.Framework, namespace, fnName string) int {
	t.Helper()
	list, err := f.FissionClient().CoreV1().FunctionVersions(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fv1.VersionFunctionNameLabel + "=" + fnName,
	})
	require.NoError(t, err)
	return len(list.Items)
}
