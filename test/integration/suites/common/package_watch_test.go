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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestPackageBuildWatch exercises `fission package create/update/rebuild
// --watch` (issue #3676): the CLI blocks until the build reaches a terminal
// state, streams the builder pod's live output while it runs, prints the
// final Status.BuildLog as the authoritative record, and exits non-zero when
// the build fails.
func TestPackageBuildWatch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "python-pkgwatch-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime, Builder: builder})
	ns.WaitForBuilderReady(t, ctx, envName)

	zipPath := framework.ZipTestDataDir(t, "python/sourcepkg", "watch-src-pkg.zip")

	// Warm-up build: the very first build against a fresh env can fail with
	// the transient kube-proxy dial timeout (see waitForPackageBuildStatus),
	// which that helper retries. Doing one ordinary build first means the
	// --watch subtests below observe real build outcomes, not infra races.
	warmupPkg := "pkg-watch-warmup-" + ns.ID
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: warmupPkg, Env: envName, Src: zipPath, BuildCmd: "./build.sh",
	})
	ns.WaitForPackageBuildSucceeded(t, ctx, warmupPkg)

	// Packages created through raw CLI calls (rather than ns.CreatePackage)
	// need their own cleanup registration.
	deletePkgOnCleanup := func(t *testing.T, name string) {
		t.Cleanup(func() {
			err := f.FissionClient().CoreV1().Packages(ns.Name).Delete(context.Background(), name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				t.Logf("cleanup package %s: %v", name, err)
			}
		})
	}

	t.Run("create_watch_success", func(t *testing.T) {
		pkgName := "pkg-watch-ok-" + ns.ID
		deletePkgOnCleanup(t, pkgName)
		out := ns.CLI(t, ctx, "package", "create", "--name", pkgName, "--env", envName,
			"--src", zipPath, "--buildcmd", "./build.sh", "--watch")

		// The builder container prints its START/END markers to stdout only —
		// they never land in Status.BuildLog — so seeing one proves the live
		// stream from the builder pod worked, not just the final log print.
		assert.Contains(t, out, "========= START =========")
		// The final report is the authoritative Status.BuildLog.
		assert.Contains(t, out, "========= build log =========")
		assert.Contains(t, out, "Package '"+pkgName+"' build succeeded")

		pkg := ns.GetPackage(t, ctx, pkgName)
		assert.Equal(t, fv1.BuildStatusSucceeded, pkg.Status.BuildStatus)
	})

	t.Run("failed_build_exit_code_and_recovery", func(t *testing.T) {
		pkgName := "pkg-watch-bad-" + ns.ID
		deletePkgOnCleanup(t, pkgName)

		// A build command that cannot exist: the build must fail, the final
		// build log must be shown, and the CLI must exit non-zero — the
		// contract PR #2643 never had.
		out, err := ns.CLIExpectError(t, ctx, "package", "create", "--name", pkgName, "--env", envName,
			"--src", zipPath, "--buildcmd", "./no-such-build-script.sh", "--watch")
		require.ErrorContains(t, err, "build failed")
		assert.Contains(t, out, "========= build log =========")

		// rebuild --watch re-runs the same (still broken) build and again
		// exits non-zero at its terminal state.
		out, err = ns.CLIExpectError(t, ctx, "package", "rebuild", "--name", pkgName, "--watch")
		require.ErrorContains(t, err, "build failed")
		assert.Contains(t, out, "========= build log =========")

		// update --watch with a corrected build command follows the rebuild
		// to success.
		out = ns.CLI(t, ctx, "package", "update", "--name", pkgName,
			"--buildcmd", "./build.sh", "--watch")
		assert.Contains(t, out, "Package '"+pkgName+"' build succeeded")
		pkg := ns.GetPackage(t, ctx, pkgName)
		assert.Equal(t, fv1.BuildStatusSucceeded, pkg.Status.BuildStatus)
	})

	t.Run("update_without_rebuild_has_nothing_to_watch", func(t *testing.T) {
		out := ns.CLI(t, ctx, "package", "update", "--name", warmupPkg, "--watch")
		assert.Contains(t, out, "did not trigger a build; nothing to watch")
	})

	t.Run("watch_refuses_spec_mode", func(t *testing.T) {
		_, err := ns.CLIExpectError(t, ctx, "package", "create", "--name", "pkg-watch-spec-"+ns.ID,
			"--env", envName, "--src", zipPath, "--dry", "--watch")
		require.ErrorContains(t, err, "cannot be combined")
	})
}
