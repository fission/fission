//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
	"github.com/fission/fission/test/integration/testdata"
)

// TestSpec is the Go port of test/tests/test_specs/test_spec.sh. It exercises
// the `fission spec init / env create --spec / spec apply / fn create --spec`
// declarative workflow end to end.
//
// Implementation note: `env create --spec` and `fn create --spec` write into
// `./specs` under the *current working directory* — they don't accept
// --specdir. We use the framework's WithCWD helper to chdir into a per-test
// temp directory under a process-global mutex; concurrent non-spec tests are
// unaffected because they all pass absolute paths to the CLI.
func TestSpec(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-spec-" + ns.ID
	fnName := "spec-" + ns.ID
	routePath := "/" + fnName

	workdir := t.TempDir()
	helloBytes, err := testdata.FS.ReadFile("python/hello/hello.py")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workdir, "hello.py"), helloBytes, 0o644))

	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		for _, p := range []string{"specs/README", "specs/fission-deployment-config.yaml"} {
			_, err := os.Stat(filepath.Join(workdir, p))
			require.NoErrorf(t, err, "spec init should have created %q", p)
		}

		ns.CLI(t, ctx, "env", "create", "--spec", "--name", envName, "--image", image)
		ns.CLI(t, ctx, "spec", "apply")
		// `env list` writes its tabular output to os.Stdout (which the
		// in-process CLI helper doesn't capture), so we don't assert on
		// it. The "X resources created" lines from spec apply are enough
		// evidence; the route+invoke below is the end-to-end check.

		ns.CLI(t, ctx, "fn", "create", "--spec", "--name", fnName, "--env", envName, "--code", "hello.py")
		_, err := os.Stat(filepath.Join(workdir, "specs", "function-"+fnName+".yaml"))
		require.NoErrorf(t, err, "fn create --spec should have written function-%s.yaml", fnName)

		ns.CLI(t, ctx, "spec", "apply")
	})

	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
}
