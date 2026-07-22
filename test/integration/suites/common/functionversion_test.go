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
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionVersionPhase1 exercises the RFC-0025 phase-1 flow end to end
// against a live cluster: publish (create + idempotent republish), a
// runtime-affecting update that mints a second version, `fn versions`,
// FunctionAlias create/get, the three webhook negatives (immutability,
// alias-referenced delete guard, dangling alias target), and finally that
// deleting the alias unblocks deleting the version it pointed at.
func TestFunctionVersionPhase1(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	envName := "nodejs-fnver-" + ns.ID
	fnName := "fn-ver-" + ns.ID
	aliasName := "prod-" + ns.ID
	v1Name := fnName + "-v1"
	v2Name := fnName + "-v2"

	// Best-effort belt-and-suspenders cleanup: delete the alias before the
	// versions it may still reference, so a mid-test failure doesn't leave
	// the version delete guard blocking teardown. Registered before any of
	// these resources exist, so a not-found error on any of them is a no-op.
	// Runs before TestNamespace's own cleanup (which deletes the Function and
	// lets ownerRef GC take the rest) because it is registered later and
	// t.Cleanup runs LIFO.
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = fc.FunctionAliases(ns.Name).Delete(cctx, aliasName, metav1.DeleteOptions{})
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, v1Name, metav1.DeleteOptions{})
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, v2Name, metav1.DeleteOptions{})
	})

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	codeV1 := writeNodeReturning(t, "v1", "v1!\n")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codeV1})
	ns.WaitForFunction(t, ctx, fnName)

	// --- publish v1 ---
	// --wait: the CLI-created Package's BuildStatus is set asynchronously by
	// buildermgr's reconciler (empty at creation, then "none" for a
	// deploy-archive package with no builder); without --wait, publish can
	// race that reconciler and see ErrPackageNotReady.
	out := ns.CLI(t, ctx, "fn", "publish", "--name", fnName, "--wait")
	assert.Contains(t, out, "created "+v1Name)

	v1, err := fc.FunctionVersions(ns.Name).Get(ctx, v1Name, metav1.GetOptions{})
	require.NoErrorf(t, err, "get FunctionVersion %q", v1Name)
	assert.EqualValues(t, 1, v1.Spec.Sequence, "first published version must carry sequence 1")
	assert.NotEmptyf(t, v1.Spec.PackageDigest, "FunctionVersion %q must record a non-empty package digest", v1Name)

	// --- idempotent republish: no spec/digest change, no new version minted ---
	out = ns.CLI(t, ctx, "fn", "publish", "--name", fnName, "--wait")
	assert.Contains(t, out, "unchanged "+v1Name)

	versionSelector := labels.SelectorFromSet(labels.Set{fv1.VersionFunctionNameLabel: fnName}).String()
	list, err := fc.FunctionVersions(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: versionSelector})
	require.NoError(t, err)
	require.Lenf(t, list.Items, 1, "idempotent republish must not mint a duplicate version (got %d)", len(list.Items))

	// --- runtime-affecting update (new code -> new package digest), then publish -> v2 ---
	codeV2 := writeNodeReturning(t, "v2", "v2!\n")
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", codeV2)

	out = ns.CLI(t, ctx, "fn", "publish", "--name", fnName, "--wait")
	assert.Contains(t, out, "created "+v2Name)

	v2, err := fc.FunctionVersions(ns.Name).Get(ctx, v2Name, metav1.GetOptions{})
	require.NoErrorf(t, err, "get FunctionVersion %q", v2Name)
	assert.EqualValues(t, 2, v2.Spec.Sequence)
	assert.NotEmpty(t, v2.Spec.PackageDigest)
	assert.NotEqualf(t, v1.Spec.PackageDigest, v2.Spec.PackageDigest,
		"v1 and v2 must record different package digests after a code change")

	// --- fn versions lists both, newest last ---
	out = ns.CLICaptureStdout(t, ctx, "fn", "versions", "--name", fnName)
	assert.Contains(t, out, v1Name)
	assert.Contains(t, out, v2Name)

	// --- alias create/get, pinned at v1 ---
	out = ns.CLICaptureStdout(t, ctx, "alias", "create",
		"--name", aliasName, "--function", fnName, "--version", v1Name)
	assert.Contains(t, out, aliasName)

	out = ns.CLICaptureStdout(t, ctx, "alias", "get", "--name", aliasName)
	assert.Contains(t, out, aliasName)
	assert.Contains(t, out, fnName)
	assert.Contains(t, out, v1Name)

	// --- webhook negative 1: update of a published FunctionVersion's spec is rejected ---
	// Mutate EnvRuntimeImage rather than Sequence: Sequence is baked into the
	// object's name ("<functionName>-v<sequence>", enforced by
	// FunctionVersion.Validate — a plain field-shape rule, not the
	// immutability guard this negative targets), so changing it would trip
	// that check first and mask the assertion we actually want to exercise.
	fresh, err := fc.FunctionVersions(ns.Name).Get(ctx, v1Name, metav1.GetOptions{})
	require.NoError(t, err)
	fresh.Spec.EnvRuntimeImage = fresh.Spec.EnvRuntimeImage + "-mutated"
	_, err = fc.FunctionVersions(ns.Name).Update(ctx, fresh, metav1.UpdateOptions{})
	require.Error(t, err, "expected webhook rejection of a FunctionVersion spec update")
	assert.Contains(t, err.Error(), "a published FunctionVersion is immutable")

	// --- webhook negative 2: deleting a version still referenced by an alias is rejected ---
	err = fc.FunctionVersions(ns.Name).Delete(ctx, v1Name, metav1.DeleteOptions{})
	require.Error(t, err, "expected webhook rejection of deleting an alias-referenced FunctionVersion")
	assert.Contains(t, err.Error(), "is still referenced by FunctionAlias")

	// --- webhook negative 3: an alias pointing at a version that doesn't exist is rejected ---
	missingVersion := fnName + "-v99"
	badAliasName := "bad-" + ns.ID
	_, err = ns.CLIExpectError(t, ctx, "alias", "create",
		"--name", badAliasName, "--function", fnName, "--version", missingVersion)
	assert.Contains(t, err.Error(), "does not exist in namespace")

	// --- alias delete unblocks the version it pointed at ---
	out = ns.CLICaptureStdout(t, ctx, "alias", "delete", "--name", aliasName)
	assert.Contains(t, out, aliasName)

	err = fc.FunctionVersions(ns.Name).Delete(ctx, v1Name, metav1.DeleteOptions{})
	assert.NoErrorf(t, err, "deleting FunctionVersion %q must succeed once no alias references it", v1Name)
}
