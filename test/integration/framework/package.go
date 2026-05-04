//go:build integration

package framework

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// PackageOptions are the inputs to TestNamespace.CreatePackage. Either Src
// (source archive — built by the env's builder) or Deploy (already-built
// deploy archive) must be set, not both. Inputs may be zip files or
// glob-style patterns expanded by the CLI under cwd.
type PackageOptions struct {
	// Name of the Package CR. Required.
	Name string
	// Env is the Environment name. Required.
	Env string
	// Src is the source archive path (or glob). When set, the env's builder
	// runs to produce the deploy archive.
	Src string
	// Deploy is the deploy archive path (or glob). Bypasses the builder.
	Deploy string
	// BuildCmd is passed as `--buildcmd` for source-archive packages.
	BuildCmd string
	// DeployChecksum is the optional SHA256 the CLI compares against the
	// downloaded deploy archive when the source is a URL.
	DeployChecksum string
	// Insecure disables checksum verification on URL-based archives.
	Insecure bool
}

// CreatePackage creates a Package via `fission package create` and registers
// its deletion on the namespace cleanup chain. Use this for tests that want
// to exercise pkg-then-fn workflows separately from the bundled
// `fn create --src` shortcut.
func (ns *TestNamespace) CreatePackage(t *testing.T, ctx context.Context, opts PackageOptions) {
	t.Helper()
	require.NotEmpty(t, opts.Name, "PackageOptions.Name")
	require.NotEmpty(t, opts.Env, "PackageOptions.Env")
	require.Truef(t, (opts.Src == "") != (opts.Deploy == ""),
		"PackageOptions: exactly one of Src or Deploy must be set (got %+v)", opts)

	args := []string{"package", "create", "--name", opts.Name, "--env", opts.Env}
	if opts.Src != "" {
		args = append(args, "--src", opts.Src)
		if opts.BuildCmd != "" {
			args = append(args, "--buildcmd", opts.BuildCmd)
		}
	} else {
		args = append(args, "--deploy", opts.Deploy)
	}
	if opts.DeployChecksum != "" {
		args = append(args, "--deploychecksum", opts.DeployChecksum)
	}
	if opts.Insecure {
		args = append(args, "--insecure")
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("package "+opts.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().Packages(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}

// GetPackage returns the live Package CR by name. Use it to assert on
// fields the controller has populated (Status.BuildStatus, BuildLog, etc.).
func (ns *TestNamespace) GetPackage(t *testing.T, ctx context.Context, pkgName string) *fv1.Package {
	t.Helper()
	p, err := ns.f.fissionClient.CoreV1().Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetPackage: get package %q", pkgName)
	return p
}

// PackageDeployChecksum returns Package.Spec.Deployment.Checksum.Sum.
// Mirrors the bash one-liner:
// `kubectl get packages <name> -o jsonpath='{.spec.deployment.checksum.sum}'`.
func (ns *TestNamespace) PackageDeployChecksum(t *testing.T, ctx context.Context, pkgName string) string {
	t.Helper()
	p, err := ns.f.fissionClient.CoreV1().Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
	require.NoErrorf(t, err, "PackageDeployChecksum: get package %q", pkgName)
	return p.Spec.Deployment.Checksum.Sum
}

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
//
// When the desired terminal state is `succeeded` and the build fails with
// a known transient signature ("dial tcp ...:8000: i/o timeout" — kube-proxy
// hasn't propagated the env's builder Service yet, more common on older
// Kubernetes versions), we retry up to `maxTransientRetries` times by
// resetting Status.BuildStatus → pending. The buildermgr's UpdateFunc
// re-triggers the build (see pkg/buildermgr/pkgwatcher.go:259-262).
func (ns *TestNamespace) waitForPackageBuildStatus(t *testing.T, ctx context.Context, pkgName string, want fv1.BuildStatus, timeout time.Duration) {
	t.Helper()
	const maxTransientRetries = 3
	transientRetries := 0

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
			if want == fv1.BuildStatusFailed {
				return false, nil
			}
			if isTransientBuildError(p.Status.BuildLog) && transientRetries < maxTransientRetries {
				transientRetries++
				t.Logf("package %q transient build failure (retry %d/%d): %s",
					pkgName, transientRetries, maxTransientRetries, p.Status.BuildLog)
				p.Status.BuildStatus = fv1.BuildStatusPending
				if _, uerr := ns.f.fissionClient.CoreV1().Packages(ns.Name).Update(c, p, metav1.UpdateOptions{}); uerr != nil {
					return false, fmt.Errorf("transient retry: reset package status to pending: %w", uerr)
				}
				return false, nil
			}
			return false, fmt.Errorf("build failed; build log:\n%s", p.Status.BuildLog)
		}
		return false, nil
	})
	require.NoErrorf(t, err, "package %q never reached build status %q (last=%q, last build log: %s)",
		pkgName, want, lastStatus, lastLog)
}

// isTransientBuildError reports whether the given build log matches a
// known transient/race failure signature. The fetcher dial-timeout
// (kube-proxy lag on the env's builder Service) is the canonical case;
// add more patterns here as they're identified.
func isTransientBuildError(buildLog string) bool {
	return strings.Contains(buildLog, "dial tcp") &&
		strings.Contains(buildLog, ":8000") &&
		strings.Contains(buildLog, "i/o timeout")
}
