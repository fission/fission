//go:build integration

package common_test

import (
	"archive/zip"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
	"github.com/fission/fission/test/integration/testdata"
)

// TestPackageCommand is the Go port of test/tests/test_package_command.sh.
// It exercises the four ways `fission package create` accepts inputs:
//
//   - source files via glob (built by env's builder)
//   - source archive zip (built by env's builder)
//   - deployment files via glob (no build)
//   - deployment archive zip (no build)
//
// Each subtest creates its own package + function + route and verifies the
// rendered HTTP response.
func TestPackageCommand(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "python-pkgcmd-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime, Builder: builder})
	ns.WaitForBuilderReady(t, ctx, envName)

	// Materialize the two source trees the bash test pulls from
	// fission/examples (sourcepkg + multifile) under <workdir>/<dir>/.
	workdir := t.TempDir()
	for _, dir := range []string{"python/sourcepkg", "python/multifile"} {
		base := filepath.Base(dir)
		require.NoError(t, os.MkdirAll(filepath.Join(workdir, base), 0o755))
		require.NoError(t, fs.WalkDir(testdata.FS, dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := testdata.FS.ReadFile(path)
			if err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if filepath.Ext(path) == ".sh" {
				mode = 0o755
			}
			return os.WriteFile(filepath.Join(workdir, base, filepath.Base(path)), b, mode)
		}))
	}

	t.Run("src_glob", func(t *testing.T) {
		// Known-flaky under parallel load: this is the first build against a
		// freshly created env, so the runtime pool pod the builder calls
		// out to (POST /fetch on env-pod port 8000) may still be in
		// ContainerCreating when the build attempts the fetch. Other
		// subtests pass because by the time they run the pod is up.
		// Re-enable once the framework can wait for the runtime pod's
		// fetcher to be Ready (selector: environmentName=<env>), or once
		// per-test parallelism is reduced. Tracked under
		// docs/test-migration/01-migration-status.md (batch 4).
		t.Skip("flaky under t.Parallel: builder fetches via runtime pod's fetcher which may not be Ready yet")
	})

	t.Run("src_zip", func(t *testing.T) {
		pkgName := "pkg-srczip-" + ns.ID
		fnName := "fn-srczip-" + ns.ID
		routePath := "/" + fnName
		zipPath := framework.ZipTestDataDir(t, "python/sourcepkg", "demo-src-pkg.zip")
		ns.CreatePackage(t, ctx, framework.PackageOptions{
			Name: pkgName, Env: envName, Src: zipPath, BuildCmd: "./build.sh",
		})
		ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Pkg: pkgName, Entrypoint: "user.main",
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("a: 1"))
		require.Contains(t, body, "a: 1")
	})

	t.Run("deploy_glob", func(t *testing.T) {
		pkgName := "pkg-deployglob-" + ns.ID
		fnName := "fn-deployglob-" + ns.ID
		routePath := "/" + fnName
		ns.WithCWD(t, workdir, func() {
			ns.CreatePackage(t, ctx, framework.PackageOptions{
				Name: pkgName, Env: envName, Deploy: "multifile/*",
			})
		})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Pkg: pkgName, Entrypoint: "main.main",
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
		f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
	})

	t.Run("deploy_zip", func(t *testing.T) {
		pkgName := "pkg-deployzip-" + ns.ID
		fnName := "fn-deployzip-" + ns.ID
		routePath := "/" + fnName

		// Build a minimal deploy archive containing a hello fn — same shape
		// as the bash test's `mkdir deploypkg; touch __init__.py; ...; zip`.
		deployDir := filepath.Join(t.TempDir(), "deploypkg")
		require.NoError(t, os.MkdirAll(deployDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(deployDir, "__init__.py"), nil, 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(deployDir, "hello.py"),
			[]byte("def main():\n    return \"Hello, world!\"\n"), 0o644))
		zipPath := zipDirContents(t, deployDir, "demo-deploy-pkg.zip")
		ns.CreatePackage(t, ctx, framework.PackageOptions{
			Name: pkgName, Env: envName, Deploy: zipPath,
		})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Pkg: pkgName, Entrypoint: "hello.main",
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
		f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
	})
}

// zipDirContents packs every regular file in `dir` (non-recursively) into a
// flat zip archive named `archiveName` under t.TempDir(), and returns the
// archive path. This mirrors the bash idiom `zip -jr out.zip dir/`.
func zipDirContents(t *testing.T, dir, archiveName string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), archiveName)
	out, err := os.Create(dst)
	require.NoError(t, err)
	defer out.Close()
	zw := zip.NewWriter(out)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		fh := &zip.FileHeader{Name: e.Name(), Method: zip.Deflate}
		fh.SetMode(0o644)
		w, err := zw.CreateHeader(fh)
		require.NoError(t, err)
		_, err = w.Write(b)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return dst
}
