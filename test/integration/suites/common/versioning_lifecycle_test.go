// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// RFC-0025 Phase 4 live integration coverage: the buildermgr-hosted
// controllers layered on top of the phase-1/2/3 publish/alias/routing
// primitives already proven by functionversion_test.go,
// versioned_specialize_test.go and alias_routing_test.go --
// AutoPublishReconciler (mint a version automatically on a runtime-affecting
// update, once opted in), RetentionGCReconciler (sweep old unaliased
// versions down to a retain floor, alias references are a hard floor) and
// the env-drift half of AliasReconciler (flag an alias whose resolved
// version was published under an Environment generation the live
// Environment has since moved past). All three ship no CLI opt-in flag on
// `fission fn create`/`update` (grepped pkg/fission-cli/cmd/function and
// pkg/fission-cli/flag: no --version-mode anywhere) -- opting a Function
// into RFC-0025 versioning today means patching Spec.Versioning through the
// typed clientset, exactly like functionversion_test.go/
// versioned_specialize_test.go already reach past the CLI for setup these
// tests have no flag for. A CLI opt-in surface (e.g. `fission fn create
// --version-mode auto`) is a phase-5/docs follow-up, not phase 4's job.
package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// enableVersioning patches fnName's Spec.Versioning through the typed
// clientset (see the package doc comment above: no CLI flag exists yet) and
// returns the updated Function. Retries once on a Conflict -- CreateFunction
// itself is the only other writer of this object in these tests' lifetime,
// but the apiserver's own defaulting round-trip (Mode's
// +kubebuilder:default:=auto) can race a Get immediately after creation.
func enableVersioning(t *testing.T, ctx context.Context, ns *framework.TestNamespace, fnName string, cfg fv1.VersioningConfig) *fv1.Function {
	t.Helper()
	fc := ns.Framework().FissionClient().CoreV1()

	var updated *fv1.Function
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fn, err := fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
		if !assert.NoErrorf(c, err, "get function %q", fnName) {
			return
		}
		cfgCopy := cfg
		fn.Spec.Versioning = &cfgCopy
		got, err := fc.Functions(ns.Name).Update(ctx, fn, metav1.UpdateOptions{})
		if !assert.NoErrorf(c, err, "patch Spec.Versioning on function %q", fnName) {
			return
		}
		updated = got
	}, 30*time.Second, time.Second)
	return updated
}

// countVersions returns the number of FunctionVersions currently labeled
// for fnName -- used both to assert growth (a new version appeared) and
// stability (a bounded window produced no growth).
func countVersions(t *testing.T, ctx context.Context, ns *framework.TestNamespace, fnName string) int {
	t.Helper()
	fc := ns.Framework().FissionClient().CoreV1()
	selector := metav1.ListOptions{LabelSelector: fv1.VersionFunctionNameLabel + "=" + fnName}
	list, err := fc.FunctionVersions(ns.Name).List(ctx, selector)
	require.NoErrorf(t, err, "list function versions for %q", fnName)
	return len(list.Items)
}

// assertVersionCountStable polls countVersions for window and fails if it
// ever exceeds want -- the bounded negative assertion for "this update must
// NOT mint a new version": require.Never would only sample once at the end,
// this instead asserts on every poll tick so a version minted and then
// swept away inside the window (impossible today, but not a case this test
// wants to silently pass) would still be caught.
func assertVersionCountStable(t *testing.T, ctx context.Context, ns *framework.TestNamespace, fnName string, want int, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		got := countVersions(t, ctx, ns, fnName)
		require.LessOrEqualf(t, got, want, "function %q must not mint a version beyond the expected %d during the settle window (got %d)", fnName, want, got)
		time.Sleep(2 * time.Second)
	}
}

// ---------------------------------------------------------------------------
// 1. Auto-publish E2E
// ---------------------------------------------------------------------------

// TestAutoPublishLifecycle proves the RFC-0025 phase-4 AutoPublishReconciler
// end to end: enabling Spec.Versioning{Mode:auto} on a Function that already
// has a Package auto-mints v1 (no existing version at all always proceeds --
// see AutoPublishReconciler.Reconcile's doc comment, point 3), a subsequent
// runtime-affecting update (--code, changes the Package's digest) mints v2
// once the (builder-less, so near-instant) package build settles, and a
// non-runtime-affecting update (--idletimeout, explicitly excluded by
// versioning.RuntimeAffecting -- see pkg/versioning/classifier.go) mints
// nothing over a bounded settle window.
func TestAutoPublishLifecycle(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	envName := "nodejs-autopub-" + ns.ID
	fnName := "fn-autopub-" + ns.ID
	v1Name := fnName + "-v1"
	v2Name := fnName + "-v2"

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, v1Name, metav1.DeleteOptions{})
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, v2Name, metav1.DeleteOptions{})
	})

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	codeV0 := writeNodeReturning(t, "autopub-v0", "v0!\n")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codeV0})
	ns.WaitForFunction(t, ctx, fnName)

	// --- enable auto-publish: no prior version exists, so this alone must
	// mint v1 -- see AutoPublishReconciler.Reconcile's doc comment, point 3.
	enableVersioning(t, ctx, ns, fnName, fv1.VersioningConfig{Mode: fv1.VersioningModeAuto})

	var v1 *fv1.FunctionVersion
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got, err := fc.FunctionVersions(ns.Name).Get(ctx, v1Name, metav1.GetOptions{})
		if !assert.NoErrorf(c, err, "get auto-published FunctionVersion %q", v1Name) {
			return
		}
		v1 = got
	}, 2*time.Minute, 2*time.Second)
	assert.EqualValues(t, 1, v1.Spec.Sequence, "auto-published v1 must carry sequence 1")
	assert.NotEmptyf(t, v1.Spec.PackageDigest, "auto-published v1 must record a non-empty package digest")

	// --- runtime-affecting update (new code -> new package digest) must
	// EVENTUALLY auto-mint v2, once the package build settles. No CLI
	// publish is issued here -- the reconciler is the only thing that can
	// create v2 in this test.
	codeV1 := writeNodeReturning(t, "autopub-v1", "v1!\n")
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", codeV1)

	var v2 *fv1.FunctionVersion
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got, err := fc.FunctionVersions(ns.Name).Get(ctx, v2Name, metav1.GetOptions{})
		if !assert.NoErrorf(c, err, "get auto-published FunctionVersion %q", v2Name) {
			return
		}
		v2 = got
	}, 3*time.Minute, 2*time.Second)
	assert.EqualValues(t, 2, v2.Spec.Sequence)
	assert.NotEmpty(t, v2.Spec.PackageDigest)
	assert.NotEqualf(t, v1.Spec.PackageDigest, v2.Spec.PackageDigest,
		"v1 and v2 must record different package digests after a code change")

	require.Equal(t, 2, countVersions(t, ctx, ns, fnName), "exactly v1+v2 must exist before the non-affecting update")

	// --- non-runtime-affecting update (IdleTimeout only affects when an
	// already-warm pod is reaped -- see versioning.RuntimeAffecting) must
	// NOT mint a v3 over a bounded settle window.
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--idletimeout", "180")
	assertVersionCountStable(t, ctx, ns, fnName, 2, 30*time.Second)

	_, err := fc.FunctionVersions(ns.Name).Get(ctx, fnName+"-v3", metav1.GetOptions{})
	require.Truef(t, apierrors.IsNotFound(err), "expected no v3 to exist after a non-runtime-affecting update, got err=%v", err)
}

// ---------------------------------------------------------------------------
// 2. Retention sweep E2E
// ---------------------------------------------------------------------------

// TestRetentionSweepE2E proves the RFC-0025 phase-4 RetentionGCReconciler
// end to end, both its automatic (event-driven) sweep and `fission fn
// gc-versions`'s on-demand one, against the same SweepVersions engine
// (pkg/versioning/retentiongc.go).
//
// Versions are published in a deliberate order -- publish v1, alias it
// FIRST, then publish v2/v3/v4 -- so the oldest version is alias-protected
// (invariant V3) from the moment it stops being "newest" onward. Publishing
// all 4 before aliasing would race the reconciler's own automatic sweep:
// each FunctionVersion Create event re-triggers a sweep (versionCreatePredicate,
// CREATE-only), so by the time v4 landed an as-yet-unaliased v1 could
// already have been sweepable, and the alias create that names it would
// then hit the phase-1 webhook's "does not exist" guard. Aliasing v1 before
// any later publish makes it retained (never a GC candidate) for the rest
// of the test, independent of sweep timing.
func TestRetentionSweepE2E(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	envName := "nodejs-gc-" + ns.ID
	fnName := "fn-gc-" + ns.ID
	aliasName := "prod-gc-" + ns.ID
	v1Name := fnName + "-v1"
	v2Name := fnName + "-v2"
	v3Name := fnName + "-v3"
	v4Name := fnName + "-v4"

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = fc.FunctionAliases(ns.Name).Delete(cctx, aliasName, metav1.DeleteOptions{})
		for _, v := range []string{v1Name, v2Name, v3Name, v4Name} {
			_ = fc.FunctionVersions(ns.Name).Delete(cctx, v, metav1.DeleteOptions{})
		}
	})

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	code0 := writeNodeReturning(t, "gc-v1", "gc-v1!\n")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: code0})
	ns.WaitForFunction(t, ctx, fnName)

	// Manual mode + Retain=2: deterministic version creation (fn publish
	// --wait), no auto-publish reconciler racing the explicit publishes
	// below (auto-vs-manual is already covered by TestAutoPublishLifecycle;
	// this test isolates retention).
	retain := 2
	enableVersioning(t, ctx, ns, fnName, fv1.VersioningConfig{Mode: fv1.VersioningModeManual, Retain: &retain})

	out := ns.CLI(t, ctx, "fn", "publish", "--name", fnName, "--wait")
	assert.Contains(t, out, "created "+v1Name)

	// Alias the oldest version BEFORE any later publish -- see the func doc
	// comment above for why this ordering matters.
	out = ns.CLICaptureStdout(t, ctx, "alias", "create",
		"--name", aliasName, "--function", fnName, "--version", v1Name)
	assert.Contains(t, out, aliasName)

	for i, name := range []string{v2Name, v3Name, v4Name} {
		code := writeNodeReturning(t, "gc-v"+name, "gc-v"+name+"!\n")
		ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", code)
		out = ns.CLI(t, ctx, "fn", "publish", "--name", fnName, "--wait")
		assert.Containsf(t, out, "created "+name, "publish #%d should mint %q", i+2, name)
	}

	// EVENTUALLY: v1 (aliased) and v4/v3 (newest 2) survive; v2 (unaliased,
	// beyond the retain-2 floor) is swept by the automatic reconciler --
	// nothing here issues `fn gc-versions`.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := fc.FunctionVersions(ns.Name).Get(ctx, v2Name, metav1.GetOptions{})
		assert.Truef(c, apierrors.IsNotFound(err), "expected %q to be swept (unaliased, beyond retain=2), got err=%v", v2Name, err)
	}, 2*time.Minute, 2*time.Second)

	for _, name := range []string{v1Name, v3Name, v4Name} {
		_, err := fc.FunctionVersions(ns.Name).Get(ctx, name, metav1.GetOptions{})
		assert.NoErrorf(t, err, "expected %q to survive the automatic sweep", name)
	}

	// --- manual `fn gc-versions --keep 1` sweeps further: v3 is now the
	// only unaliased, non-newest-1 version left (v1 stays retained --
	// alias-referenced, independent of --keep; v4 is the newest 1).
	out = ns.CLICaptureStdout(t, ctx, "fn", "gc-versions", "--name", fnName, "--keep", "1")
	assert.Contains(t, out, "deleted 1")

	_, err := fc.FunctionVersions(ns.Name).Get(ctx, v3Name, metav1.GetOptions{})
	require.Truef(t, apierrors.IsNotFound(err), "expected %q to be deleted by `fn gc-versions --keep 1`, got err=%v", v3Name, err)

	for _, name := range []string{v1Name, v4Name} {
		_, err := fc.FunctionVersions(ns.Name).Get(ctx, name, metav1.GetOptions{})
		assert.NoErrorf(t, err, "expected %q to survive `fn gc-versions --keep 1` (alias-referenced or newest)", name)
	}

	alias, err := fc.FunctionAliases(ns.Name).Get(ctx, aliasName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equalf(t, v1Name, alias.Spec.Version, "alias must still pin %q after both sweeps", v1Name)
}

// ---------------------------------------------------------------------------
// 3. Env drift E2E
// ---------------------------------------------------------------------------

// TestEnvDriftE2E proves the RFC-0025 phase-4 env-drift half of
// AliasReconciler end to end: an Environment spec update (which bumps its
// Generation but touches no Function -- "Environment & Package changes
// across the version boundary", see FunctionAliasConditionEnvDrift's doc
// comment) makes an already-resolved FunctionAlias's EnvDrift condition
// flip True, surfaced both by `fission fn rollback`'s non-blocking WARNING
// and by `fission env impact`'s batch DRIFT column.
func TestEnvDriftE2E(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	envName := "nodejs-drift-" + ns.ID
	fnName := "fn-drift-" + ns.ID
	aliasName := "prod-drift-" + ns.ID
	v1Name := fnName + "-v1"

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = fc.FunctionAliases(ns.Name).Delete(cctx, aliasName, metav1.DeleteOptions{})
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, v1Name, metav1.DeleteOptions{})
	})

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image, Poolsize: 2})

	code := writeNodeReturning(t, "drift-v1", "drift-v1!\n")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: code})
	ns.WaitForFunction(t, ctx, fnName)

	out := ns.CLI(t, ctx, "fn", "publish", "--name", fnName, "--wait")
	assert.Contains(t, out, "created "+v1Name)

	ns.CLICaptureStdout(t, ctx, "alias", "create",
		"--name", aliasName, "--function", fnName, "--version", v1Name)

	// Wait for the alias to resolve before drifting the environment -- the
	// EnvDrift condition is only assessable once Resolved (see
	// AliasReconciler.applyEnvDrift's "absence means not assessable"
	// contract).
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		alias, err := fc.FunctionAliases(ns.Name).Get(ctx, aliasName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		assert.Equalf(c, v1Name, alias.Status.ResolvedVersion, "alias %q must resolve to %q", aliasName, v1Name)
	}, 60*time.Second, time.Second)

	envBefore, err := fc.Environments(ns.Name).Get(ctx, envName, metav1.GetOptions{})
	require.NoError(t, err)

	// --- drift the environment: bump Poolsize, which bumps Generation but
	// touches no Function (see the RFC-0025 "version boundary" doc comment
	// above).
	ns.CLI(t, ctx, "env", "update", "--name", envName, "--poolsize", "3")

	envAfter, err := fc.Environments(ns.Name).Get(ctx, envName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Greaterf(t, envAfter.Generation, envBefore.Generation, "env update must bump Generation")

	// --- EVENTUALLY the alias's EnvDrift condition flips True ---
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		alias, err := fc.FunctionAliases(ns.Name).Get(ctx, aliasName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		cond := apimeta.FindStatusCondition(alias.Status.Conditions, fv1.FunctionAliasConditionEnvDrift)
		if !assert.NotNilf(c, cond, "alias %q must carry an EnvDrift condition once assessable", aliasName) {
			return
		}
		assert.Equalf(c, metav1.ConditionTrue, cond.Status, "EnvDrift condition status")
		assert.Equalf(c, fv1.FunctionAliasReasonEnvGenerationDrift, cond.Reason, "EnvDrift condition reason")
	}, 90*time.Second, 2*time.Second)

	// --- `fission fn rollback --to <the drifted target>` surfaces the
	// non-blocking WARNING (rollback.go's warnEnvDrift) ---
	rollbackOut := ns.CLICaptureStdout(t, ctx, "fn", "rollback", "--name", fnName, "--alias", aliasName, "--to", v1Name)
	assert.Contains(t, rollbackOut, "WARNING")
	assert.Contains(t, rollbackOut, "was published under env")
	assert.Contains(t, rollbackOut, "live env is generation")

	// --- `fission env impact --name <env>` lists the function+alias with
	// DRIFT=True ---
	impactOut := ns.CLICaptureStdout(t, ctx, "env", "impact", "--name", envName)
	assert.Contains(t, impactOut, fnName)
	assert.Contains(t, impactOut, aliasName)
	assert.Contains(t, impactOut, "True")
}
