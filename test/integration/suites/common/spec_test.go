//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
	"github.com/fission/fission/test/integration/testdata"
)

// TestSpec is the Go port of test/tests/test_specs/test_spec.sh. It exercises
// the `fission spec init / env create --spec / spec apply / fn create --spec`
// declarative workflow end to end:
//
//   - spec init bootstraps a specs/ directory with README and DeploymentConfig.
//   - env create --spec writes an Environment yaml; spec apply reconciles it.
//   - fn create --spec writes Package + Function yamls; spec apply uploads
//     the inline code archive and creates the resources.
//   - GET via the router returns the function body.
//
// We pass --specdir explicitly (an absolute per-test path under t.TempDir)
// so concurrent specs tests don't race over the process cwd.
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
	specDir := filepath.Join(workdir, "specs")
	helloBytes, err := testdata.FS.ReadFile("python/hello/hello.py")
	require.NoError(t, err)
	helloPath := filepath.Join(workdir, "hello.py")
	require.NoError(t, os.WriteFile(helloPath, helloBytes, 0o644))

	ns.CLI(t, ctx, "spec", "init", "--specdir", specDir)
	for _, p := range []string{"README", "fission-deployment-config.yaml"} {
		_, err := os.Stat(filepath.Join(specDir, p))
		require.NoErrorf(t, err, "spec init should have created %q", p)
	}

	ns.CLI(t, ctx, "env", "create", "--spec", "--specdir", specDir, "--name", envName, "--image", image)
	ns.CLI(t, ctx, "spec", "apply", "--specdir", specDir)

	// `env list` is a smoke check that the env reached the cluster.
	envs := ns.CLI(t, ctx, "env", "list")
	assert.Contains(t, envs, envName)

	ns.CLI(t, ctx, "fn", "create", "--spec", "--specdir", specDir,
		"--name", fnName, "--env", envName, "--code", helloPath)
	for _, glob := range []string{"function-" + fnName + ".yaml"} {
		_, err := os.Stat(filepath.Join(specDir, glob))
		require.NoErrorf(t, err, "fn create --spec should have written %q", glob)
	}

	ns.CLI(t, ctx, "spec", "apply", "--specdir", specDir)
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")
}
