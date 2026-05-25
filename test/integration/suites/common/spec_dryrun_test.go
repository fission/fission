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

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestSpecApply_DryRun verifies `fission spec apply --dry-run` previews the
// would-be changes without touching the cluster, and that a real apply makes
// the subsequent dry-run stop reporting creates. It uses a --code (literal
// deploy) function so no builder/runtime image is required — nothing is ever
// built or specialized, only the spec reconcile/diff is exercised.
func TestSpecApply_DryRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	envName := "dryrun-env-" + ns.ID
	fnName := "dryrun-fn-" + ns.ID

	workdir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workdir, "hello.js"),
		[]byte("module.exports = async function(){ return {status:200, body:'hi'} }\n"), 0o644))

	// Build the spec directory (local only — no cluster writes yet).
	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		ns.CLI(t, ctx, "env", "create", "--spec", "--name", envName, "--image", "fission/node-env")
		ns.CLI(t, ctx, "fn", "create", "--spec", "--name", fnName, "--env", envName, "--code", "hello.js")

		// 1) Dry-run on a fresh spec previews creates and changes nothing.
		out := ns.CLICaptureStdout(t, ctx, "spec", "apply", "--dry-run")
		require.Contains(t, out, "would be created")
		require.Contains(t, out, "(dry run - no changes made)")
	})

	// The dry-run must not have created anything on the cluster.
	_, err := fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "dry-run must not create the function (err=%v)", err)
	_, err = fc.Environments(ns.Name).Get(ctx, envName, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "dry-run must not create the environment (err=%v)", err)

	// 2) Real apply, then a second dry-run is idempotent w.r.t. creates.
	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "apply")
		t.Cleanup(func() {
			dctx, dcancel := context.WithTimeout(context.Background(), time.Minute)
			defer dcancel()
			ns.WithCWD(t, workdir, func() { _ = ns.CLI(t, dctx, "spec", "destroy") })
		})

		out := ns.CLICaptureStdout(t, ctx, "spec", "apply", "--dry-run")
		require.NotContains(t, out, "would be created",
			"after a real apply, a dry-run should not report creates")
		require.Contains(t, out, "(dry run - no changes made)")
	})

	// And the real apply did create the function.
	_, err = fc.Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoError(t, err, "real apply should have created the function")
}
