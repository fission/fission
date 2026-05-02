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

// TestPythonEnv is the Go port of test/tests/test_environments/test_python_env.sh.
// It exercises the python env's entrypoint resolution against five inputs:
//
//   - v1 api: code-only (no builder).
//   - v2 api with no entrypoint: defaults to main.main.
//   - v2 api with --entrypoint func: resolves to main.func.
//   - v2 api with --entrypoint foo.bar: resolves across modules.
//   - v2 api with --entrypoint sub_mod.altmain.entrypoint: deep dotted path.
//
// All v2 cases share one prebuilt source-archive package.
func TestPythonEnv(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envV1 := "python-v1-" + ns.ID
	envV2 := "python-v2-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envV1, Image: runtime})
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envV2, Image: runtime, Builder: builder})
	ns.WaitForBuilderReady(t, ctx, envV2)

	// Build the shared package from python/env_test/.
	srcZip := zipTestDataTree(t, "python/env_test", "python-src-pkg.zip")
	pkgName := "py-env-pkg-" + ns.ID
	ns.CreatePackage(t, ctx, framework.PackageOptions{Name: pkgName, Env: envV2, Src: srcZip})
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)

	// v1 api: hello.py, no builder, default entrypoint resolution.
	helloPath := framework.WriteTestData(t, "python/hello/hello.py")

	// `name` is the t.Run label; `slug` goes into resource names and must
	// be RFC 1123 (lowercase alnum + hyphens — no underscores).
	cases := []struct {
		name       string
		slug       string
		fnSetup    func(t *testing.T, fnName string)
		expectBody string
	}{
		{
			name: "v1-api", slug: "v1api",
			fnSetup: func(t *testing.T, fnName string) {
				ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envV1, Code: helloPath})
			},
			expectBody: "hello",
		},
		{
			name: "v2-default-entrypoint", slug: "v2default",
			fnSetup: func(t *testing.T, fnName string) {
				ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Pkg: pkgName})
			},
			expectBody: "THIS_IS_MAIN_MAIN",
		},
		{
			name: "v2-entrypoint-func", slug: "v2func",
			fnSetup: func(t *testing.T, fnName string) {
				ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Pkg: pkgName, Entrypoint: "func"})
			},
			expectBody: "THIS_IS_MAIN_FUNC",
		},
		{
			name: "v2-entrypoint-foo-bar", slug: "v2foobar",
			fnSetup: func(t *testing.T, fnName string) {
				ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Pkg: pkgName, Entrypoint: "foo.bar"})
			},
			expectBody: "THIS_IS_FOO_BAR",
		},
		{
			name: "v2-entrypoint-dotted", slug: "v2dotted",
			fnSetup: func(t *testing.T, fnName string) {
				ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Pkg: pkgName, Entrypoint: "sub_mod.altmain.entrypoint"})
			},
			expectBody: "THIS_IS_ALTMAIN_ENTRYPOINT",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fnName := "fn-py-" + tc.slug + "-" + ns.ID
			routePath := "/" + fnName
			tc.fnSetup(t, fnName)
			ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
			body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains(tc.expectBody))
			require.Contains(t, body, tc.expectBody)
		})
	}
}

// zipTestDataTree packs a recursive embedded directory into a zip, preserving
// relative paths from the embedDir root. Use this for source archives where
// the language runtime needs to import sub-packages (e.g. python sub_mod.*).
// File modes: `*.sh` → 0755, others → 0644 (embed.FS strips file modes).
//
// This sibling to framework.ZipTestDataDir keeps the original tree shape;
// the framework helper flattens with `zip -j` semantics.
func zipTestDataTree(t *testing.T, embedDir, archiveName string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), archiveName)
	out, err := os.Create(dst)
	require.NoError(t, err)
	defer out.Close()
	zw := zip.NewWriter(out)
	require.NoError(t, fs.WalkDir(testdata.FS, embedDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := testdata.FS.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(embedDir, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if filepath.Ext(p) == ".sh" {
			mode = 0o755
		}
		fh := &zip.FileHeader{Name: rel, Method: zip.Deflate}
		fh.SetMode(mode)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}))
	require.NoError(t, zw.Close())
	return dst
}
