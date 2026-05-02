//go:build integration

package common_test

import (
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

// TestSpecMultifile is the Go port of test/tests/test_specs/test_spec_multifile.sh.
// It exercises the spec workflow with a multi-file Python deploy archive
// (the CLI's `--deploy "multifile/*"` glob), pulling several .py + .txt files
// into one package, with a Python env+builder.
func TestSpecMultifile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "python-multi-" + ns.ID
	fnName := "spec-multi-" + ns.ID
	routePath := "/" + fnName

	workdir := t.TempDir()
	specDir := filepath.Join(workdir, "specs")

	// Materialize testdata/python/multifile/* under <workdir>/multifile/
	// so the CLI's --deploy "multifile/*" glob (resolved relative to
	// specdir/.. = workdir) finds them.
	multiDir := filepath.Join(workdir, "multifile")
	require.NoError(t, os.MkdirAll(multiDir, 0o755))
	require.NoError(t, fs.WalkDir(testdata.FS, "python/multifile", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := testdata.FS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(multiDir, filepath.Base(path)), b, 0o644)
	}))

	ns.CLI(t, ctx, "spec", "init", "--specdir", specDir)
	ns.CLI(t, ctx, "env", "create", "--spec", "--specdir", specDir,
		"--name", envName, "--image", runtime, "--builder", builder)
	ns.CLI(t, ctx, "fn", "create", "--spec", "--specdir", specDir,
		"--name", fnName, "--env", envName,
		"--deploy", filepath.Join(multiDir, "*"),
		"--entrypoint", "main.main")
	ns.CLI(t, ctx, "spec", "apply", "--specdir", specDir)

	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")
}
