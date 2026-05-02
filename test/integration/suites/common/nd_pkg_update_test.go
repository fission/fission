//go:build integration

package common_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestNDPackageUpdate is the Go port of test_fn_update/test_nd_pkg_update.sh.
// Creates a newdeploy function with a deploy archive, swaps the archive
// content, and verifies the router serves the updated body.
func TestNDPackageUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "python-ndpkg-" + ns.ID
	fnName := "fn-ndpkg-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder,
		MinCPU: 40, MaxCPU: 80, MinMemory: 64, MaxMemory: 128,
		Poolsize: 2,
	})

	// Use --deploy (already-built archive, no build). Bash also uses
	// --deploy here; previously we used --src which triggered a build and
	// hit the "runtime pod not ready" race.
	zipV1 := buildHelloZip(t, "Hello, world!")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Deploy: zipV1, Entrypoint: "hello.main",
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 4, TargetCPU: 50,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))

	// Update the deploy archive contents and re-apply.
	zipV2 := buildHelloZip(t, "Hello, fission!")
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--deploy", zipV2,
		"--entrypoint", "hello.main", "--executortype", "newdeploy",
		"--minscale", "1", "--maxscale", "4", "--targetcpu", "50")
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("fission"))
}

// buildHelloZip writes a tiny hello.py returning `body` and zips it into a
// flat archive under t.TempDir, returning the on-disk path.
func buildHelloZip(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "hello.py")
	require.NoError(t, os.WriteFile(srcPath,
		[]byte("def main():\n    return \""+body+"\""), 0o644))
	zipPath := filepath.Join(dir, "deploy.zip")
	out, err := os.Create(zipPath)
	require.NoError(t, err)
	defer out.Close()
	zw := zip.NewWriter(out)
	fh := &zip.FileHeader{Name: "hello.py", Method: zip.Deflate}
	fh.SetMode(0o644)
	w, err := zw.CreateHeader(fh)
	require.NoError(t, err)
	_, err = w.Write([]byte("def main():\n    return \"" + body + "\""))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return zipPath
}
