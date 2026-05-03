//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestSpecArchive is the Go port of test_specs/test_spec_archive.
// Mirrors the bash flow:
//
//   1. Materialize the env + two Package + two Function spec yamls into
//      a workdir, alongside a `func/` source tree (deploy.js, source.js,
//      package.json) that the spec's ArchiveUploadSpec.include `func/*`
//      refers to.
//   2. `fission spec apply` from the workdir.
//   3. The deployarchive function is a deploy archive (no build) — hit it
//      immediately.
//   4. The sourcearchive function is a source archive — wait for the
//      buildermgr to flip its package status to succeeded, then hit it.
//
// MaterializeSpecs rewrites all hardcoded names + the deployment-config
// UID, so this can run alongside other tests in `default`.
func TestSpecArchive(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	_ = f.Images().RequireNode(t)
	// Source archive needs the node builder to compile.
	_ = f.Images().NodeBuilder

	ns := f.NewTestNamespace(t)

	envName := "dfbnode-" + ns.ID
	pkgDeploy := "deployarchive-" + ns.ID
	pkgSource := "sourcearchive-" + ns.ID
	archDeploy := "fns-deploy-arch-" + ns.ID
	archSource := "fns-src-arch-" + ns.ID
	uid := framework.NewSpecUID(t)

	repls := map[string]string{
		"dummyfoobarnode":           envName,
		"deployarchive":             pkgDeploy,
		"sourcearchive":             pkgSource,
		"functions-deploy-archive":  archDeploy,
		"functions-source-archive":  archSource,
		"name: test-spec-archive":   "name: test-spec-archive-" + ns.ID,
		"04b21526-8873-4dc2-b897-e87ed5347670": uid,
	}

	workdir := t.TempDir()
	framework.MaterializeSpecs(t, "nodejs/spec_archive", repls, workdir)

	// The archive specs include `func/*` — write the source tree.
	funcDir := filepath.Join(workdir, "func")
	require.NoError(t, os.Mkdir(funcDir, 0o755))
	const helloJS = `module.exports = async function(context) {
    return { status: 200, body: "hello, world!\n" };
}
`
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "deploy.js"), []byte(helloJS), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "source.js"), []byte(helloJS), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "package.json"),
		[]byte(`{"name":"hello","version":"1.0.0","description":"hello function"}`), 0o644))

	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "apply")
	})
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer dcancel()
		ns.WithCWD(t, workdir, func() {
			_ = ns.CLI(t, dctx, "spec", "destroy")
		})
	})

	// Phase 1 — deploy archive is no-op build, function should serve immediately.
	deployBody := f.Router(t).GetEventually(t, ctx, "/fission-function/"+pkgDeploy,
		framework.BodyContains("hello"))
	require.True(t, strings.Contains(strings.ToLower(deployBody), "hello"),
		"deployarchive fn body missing 'hello': %q", deployBody)

	// Phase 2 — source archive needs a build. The package status starts
	// "pending"; wait for it to flip to succeeded.
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgSource)

	// Belt-and-suspenders: make sure the package status really is succeeded
	// before hitting the function.
	pkg := ns.GetPackage(t, ctx, pkgSource)
	require.Equalf(t, fv1.BuildStatusSucceeded, pkg.Status.BuildStatus,
		"sourcearchive package %q final status %q (log: %s)",
		pkgSource, pkg.Status.BuildStatus, pkg.Status.BuildLog)

	sourceBody := f.Router(t).GetEventually(t, ctx, "/fission-function/"+pkgSource,
		framework.BodyContains("hello"))
	require.True(t, strings.Contains(strings.ToLower(sourceBody), "hello"),
		"sourcearchive fn body missing 'hello': %q", sourceBody)
}
