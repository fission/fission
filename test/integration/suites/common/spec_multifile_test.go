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
//
// As with TestSpec, the spec subcommands resolve `./specs` and the
// `--deploy` glob relative to cwd, so the body runs inside ns.WithCWD.
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

	// Materialize testdata/python/multifile/* under <workdir>/multifile/
	// so the CLI's --deploy "multifile/*" glob (resolved relative to cwd)
	// finds the files at apply time.
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

	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		ns.CLI(t, ctx, "env", "create", "--spec",
			"--name", envName, "--image", runtime, "--builder", builder)
		ns.CLI(t, ctx, "fn", "create", "--spec",
			"--name", fnName, "--env", envName,
			"--deploy", "multifile/*",
			"--entrypoint", "main.main")
		ns.CLI(t, ctx, "spec", "apply")
	})

	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
	// GetEventually's BodyContains is case-insensitive; the multifile
	// function returns "Hello, world!\n" rendered from message.txt.
	f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
}
