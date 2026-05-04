//go:build integration

package common_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestKubectlApply is the Go port of test_kubectl/test_kubectl.sh.
//
// The bash version exercises a kubectl apply / kubectl replace round-trip
// against intentionally-malformed Package CRs:
//
//  1. apply env + package (URL has typo `hello.gogo` → 404)
//  2. wait for build to fail
//  3. sed-fix the URL on disk; apply again — bash expects the build to
//     stay failed (the test was written before /status subresource enablement,
//     so apply preserved Status); replace finally re-triggers the build
//
// We don't replicate the apply-vs-replace nuance — that's a Kubernetes
// status-subresource semantic, not a Fission behavior. The Fission-relevant
// signal is "broken URL → build fails; corrected URL → build succeeds; the
// function then serves traffic." We exercise the same CRD path via the
// fission clientset, which is exactly what the bash test was using
// kubectl apply for.
func TestKubectlApply(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireGo(t)
	builder := f.Images().RequireGoBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "go-kubectl-" + ns.ID
	pkgName := "pkg-kubectl-" + ns.ID
	fnName := "fn-kubectl-" + ns.ID

	// Use the framework's CreateEnv so the auto-wait for builder Endpoint
	// readiness covers the upcoming source-URL build.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder, Period: 5,
	})

	const goodURL = "https://raw.githubusercontent.com/fission/examples/main/go/hello-world/hello.go"
	const badURL = "https://raw.githubusercontent.com/fission/examples/main/go/hello-world/hello.gogo"

	// Phase 1 — package with bad URL; build should fail.
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: pkgName, Namespace: ns.Name},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: envName, Namespace: ns.Name},
			Source: fv1.Archive{
				Type: fv1.ArchiveTypeUrl,
				URL:  badURL,
			},
		},
	}
	created, err := f.FissionClient().CoreV1().Packages(ns.Name).Create(ctx, pkg, metav1.CreateOptions{})
	require.NoErrorf(t, err, "create package %q with bad URL", pkgName)
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_ = f.FissionClient().CoreV1().Packages(ns.Name).Delete(dctx, pkgName, metav1.DeleteOptions{})
	})
	t.Logf("package %q created with bad URL, generation=%d", created.Name, created.Generation)

	ns.WaitForPackageBuildStatus(t, ctx, pkgName, fv1.BuildStatusFailed, 3*time.Minute)

	// Phase 2 — patch URL to the working file. Re-fetch first so we
	// have the latest ResourceVersion, then update Spec.Source.URL.
	//
	// Critical: the buildermgr only triggers a (re)build when
	// Status.BuildStatus == "pending" (see pkg/buildermgr/pkgwatcher.go:259-262
	// and the long-standing TODO above it about /status subresource).
	// A plain Spec-only Update would leave status at "failed" and the
	// controller would skip the new build. Bash uses `kubectl replace`
	// which round-trips the whole object including Status; we mimic
	// that by explicitly resetting BuildStatus to pending before Update.
	current, err := f.FissionClient().CoreV1().Packages(ns.Name).Get(ctx, pkgName, metav1.GetOptions{})
	require.NoError(t, err)
	since := current.Status.LastUpdateTimestamp
	current.Spec.Source.URL = goodURL
	current.Status.BuildStatus = fv1.BuildStatusPending
	_, err = f.FissionClient().CoreV1().Packages(ns.Name).Update(ctx, current, metav1.UpdateOptions{})
	require.NoError(t, err, "update package %q to good URL + pending", pkgName)

	ns.WaitForPackageRebuiltSince(t, ctx, pkgName, since)

	// Phase 3 — wire up a Function pointing at the fixed package, no
	// HTTPTrigger needed: the router exposes /fission-function/<fn>
	// internally, which is what `fission fn test` hits.
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Pkg: pkgName, Entrypoint: "Handler",
	})

	body := f.Router(t).GetEventually(t, ctx, "/fission-function/"+fnName,
		framework.BodyContains("Hello"))
	require.True(t, strings.Contains(body, "Hello"),
		"expected /fission-function/%s body to contain Hello, got %q", fnName, body)
}
