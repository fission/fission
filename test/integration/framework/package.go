//go:build integration

package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// WaitForPackageBuildSucceeded polls Package.Status.BuildStatus until it
// reaches "succeeded" (per fv1.BuildStatusSucceeded). Failure / timeout /
// terminal "failed" status all become t.Fatal. Mirrors the bash `waitBuild`
// helper but distinguishes timeouts from explicit build failures.
func (ns *TestNamespace) WaitForPackageBuildSucceeded(t *testing.T, ctx context.Context, pkgName string) {
	t.Helper()
	ns.waitForPackageBuildStatus(t, ctx, pkgName, fv1.BuildStatusSucceeded, 3*time.Minute)
}

// PackageBuildTimestamp returns the current Package.Status.LastUpdateTimestamp.
// Capture it before triggering a rebuild (e.g. `fn update --src`); pass it to
// WaitForPackageRebuiltSince to detect that the rebuild has actually
// completed (not just observed the prior succeeded status).
func (ns *TestNamespace) PackageBuildTimestamp(t *testing.T, ctx context.Context, pkgName string) metav1.Time {
	t.Helper()
	p, err := ns.f.fissionClient.CoreV1().Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
	require.NoErrorf(t, err, "PackageBuildTimestamp: get package %q", pkgName)
	return p.Status.LastUpdateTimestamp
}

// WaitForPackageRebuiltSince polls until the package has been re-built, where
// "re-built" means BuildStatus == succeeded AND LastUpdateTimestamp > since.
//
// `fission fn update --src` patches the existing Package CR in place (the
// CR name does not change). The buildermgr controller observes the patch
// and re-runs the build, advancing LastUpdateTimestamp on success. This
// helper is race-free: it ignores any "succeeded" status that was already
// present from the prior build.
func (ns *TestNamespace) WaitForPackageRebuiltSince(t *testing.T, ctx context.Context, pkgName string, since metav1.Time) {
	t.Helper()
	var lastStatus fv1.BuildStatus
	var lastTs metav1.Time
	var lastLog string
	err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 3*time.Minute, true, func(c context.Context) (bool, error) {
		p, err := ns.f.fissionClient.CoreV1().Packages(ns.Name).Get(c, pkgName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		lastStatus = p.Status.BuildStatus
		lastTs = p.Status.LastUpdateTimestamp
		lastLog = p.Status.BuildLog
		if p.Status.BuildStatus == fv1.BuildStatusFailed && p.Status.LastUpdateTimestamp.After(since.Time) {
			return false, fmt.Errorf("rebuild failed; build log:\n%s", p.Status.BuildLog)
		}
		return p.Status.BuildStatus == fv1.BuildStatusSucceeded && p.Status.LastUpdateTimestamp.After(since.Time), nil
	})
	require.NoErrorf(t, err, "package %q never rebuilt after %s (last status=%q, last ts=%s, last build log: %s)",
		pkgName, since, lastStatus, lastTs, lastLog)
}

// WaitForPackageBuildStatus polls until the package reaches the specified
// terminal build status. Use this when a test wants to assert on a non-success
// terminal state (e.g. BuildStatusFailed for negative tests).
func (ns *TestNamespace) WaitForPackageBuildStatus(t *testing.T, ctx context.Context, pkgName string, status fv1.BuildStatus, timeout time.Duration) {
	t.Helper()
	ns.waitForPackageBuildStatus(t, ctx, pkgName, status, timeout)
}

// waitForPackageBuildStatus is the shared poll body. It uses
// wait.PollUntilContextTimeout directly (rather than require.EventuallyWithT)
// because we need to *early-exit* with the captured BuildLog as soon as the
// package reaches a different terminal state — testify's Eventually variants
// can't bail before the timeout.
func (ns *TestNamespace) waitForPackageBuildStatus(t *testing.T, ctx context.Context, pkgName string, want fv1.BuildStatus, timeout time.Duration) {
	t.Helper()
	var lastStatus fv1.BuildStatus
	var lastLog string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(c context.Context) (bool, error) {
		p, err := ns.f.fissionClient.CoreV1().Packages(ns.Name).Get(c, pkgName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		lastStatus = p.Status.BuildStatus
		lastLog = p.Status.BuildLog
		switch p.Status.BuildStatus {
		case want:
			return true, nil
		case fv1.BuildStatusFailed:
			if want != fv1.BuildStatusFailed {
				return false, fmt.Errorf("build failed; build log:\n%s", p.Status.BuildLog)
			}
		}
		return false, nil
	})
	require.NoErrorf(t, err, "package %q never reached build status %q (last=%q, last build log: %s)",
		pkgName, want, lastStatus, lastLog)
}
