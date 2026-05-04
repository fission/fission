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

	"github.com/fission/fission/test/integration/framework"
	"github.com/fission/fission/test/integration/testdata"
)

// TestSecretConfigMap is the Go port of test_secret_cfgmap/test_secret_cfgmap.sh.
// Six sub-cases plus an empty-mounts smoke:
//
//   - secret: function reads a single mounted Secret value.
//   - multi_secret: function reads two mounted Secrets.
//   - secret_newdeploy: same as secret but executor=newdeploy and the
//     Secret is updated to a new value before the function is created
//     (so the pod starts with the new value mounted).
//   - configmap, multi_configmap, configmap_newdeploy: symmetric for ConfigMaps.
//   - empty: function with neither configmap nor secret mounted; verifies
//     /configs and /secrets are empty.
//
// The subtests share one Environment and the four Kubernetes-side
// resources (two Secrets, two ConfigMaps); each subtest creates its own
// Function + HTTPTrigger so they can run independently.
func TestSecretConfigMap(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-cs-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	sec1 := "sec1-" + ns.ID
	sec2 := "sec2-" + ns.ID
	cm1 := "cm1-" + ns.ID
	cm2 := "cm2-" + ns.ID

	// Original values.
	ns.CreateSecret(t, ctx, sec1, map[string]string{"TEST_KEY": "TESTVALUE"})
	ns.CreateSecret(t, ctx, sec2, map[string]string{"TEST_KEY1": "TESTVALUE1"})
	ns.CreateConfigMap(t, ctx, cm1, map[string]string{"TEST_KEY": "TESTVALUE"})
	ns.CreateConfigMap(t, ctx, cm2, map[string]string{"TEST_KEY1": "TESTVALUE1"})

	t.Run("secret", func(t *testing.T) {
		fnName := "fnsec-" + ns.ID
		code := writeTplPy(t, "secret.py.template", "secret.py", map[string]string{"FN_SECRET": sec1})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: envName, Code: code, Secrets: []string{sec1},
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("TESTVALUE"))
		require.Contains(t, body, "TESTVALUE")
	})

	t.Run("multi_secret", func(t *testing.T) {
		fnName := "fnmsec-" + ns.ID
		code := writeTplPy(t, "multisecret.py.template", "multisecret.py", map[string]string{
			"FN_SECRET": sec1, "FN_SECRET1": sec2,
		})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: envName, Code: code, Secrets: []string{sec1, sec2},
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("TESTVALUE-TESTVALUE1"))
		require.Contains(t, body, "TESTVALUE-TESTVALUE1")
	})

	t.Run("secret_newdeploy", func(t *testing.T) {
		// Bash patches the existing Secret to NEWVAL and creates a fresh
		// fn with executor=newdeploy. We do the equivalent: a fresh
		// Secret with the new value (avoids stomping on the secret used
		// by the parallel sibling subtests).
		secNew := "secnew-" + ns.ID
		ns.CreateSecret(t, ctx, secNew, map[string]string{"TEST_KEY": "NEWVAL"})

		fnName := "fnsecnd-" + ns.ID
		code := writeTplPy(t, "secret.py.template", "secret.py", map[string]string{"FN_SECRET": secNew})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: envName, Code: code,
			Secrets: []string{secNew}, ExecutorType: "newdeploy",
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("NEWVAL"))
		require.Contains(t, body, "NEWVAL")
	})

	t.Run("configmap", func(t *testing.T) {
		fnName := "fncm-" + ns.ID
		code := writeTplPy(t, "cfgmap.py.template", "cfgmap.py", map[string]string{"FN_CFGMAP": cm1})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: envName, Code: code, ConfigMaps: []string{cm1},
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("TESTVALUE"))
		require.Contains(t, body, "TESTVALUE")
	})

	t.Run("multi_configmap", func(t *testing.T) {
		fnName := "fnmcm-" + ns.ID
		code := writeTplPy(t, "multicfgmap.py.template", "multicfgmap.py", map[string]string{
			"FN_CFGMAP": cm1, "FN_CFGMAP1": cm2,
		})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: envName, Code: code, ConfigMaps: []string{cm1, cm2},
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("TESTVALUE-TESTVALUE1"))
		require.Contains(t, body, "TESTVALUE-TESTVALUE1")
	})

	t.Run("configmap_newdeploy", func(t *testing.T) {
		cmNew := "cmnew-" + ns.ID
		ns.CreateConfigMap(t, ctx, cmNew, map[string]string{"TEST_KEY": "NEWVAL"})

		fnName := "fncmnd-" + ns.ID
		code := writeTplPy(t, "cfgmap.py.template", "cfgmap.py", map[string]string{"FN_CFGMAP": cmNew})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: envName, Code: code,
			ConfigMaps: []string{cmNew}, ExecutorType: "newdeploy",
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("NEWVAL"))
		require.Contains(t, body, "NEWVAL")
	})

	t.Run("empty", func(t *testing.T) {
		fnName := "fnempty-" + ns.ID
		code := writeEmptyPy(t)
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: code})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("yes"))
		require.Contains(t, body, "yes")
	})
}

// writeTplPy reads a vendored template, applies the substitutions, and
// writes the result under t.TempDir.
func writeTplPy(t *testing.T, tplName, outName string, subs map[string]string) string {
	t.Helper()
	b, err := testdata.FS.ReadFile("python/cfgmap_secret/" + tplName)
	require.NoErrorf(t, err, "read %s", tplName)
	body := string(b)
	for k, v := range subs {
		body = strings.ReplaceAll(body, "{{ "+k+" }}", v)
	}
	dst := filepath.Join(t.TempDir(), outName)
	require.NoError(t, os.WriteFile(dst, []byte(body), 0o644))
	return dst
}

// writeEmptyPy writes the vendored empty.py fixture (no template subs).
func writeEmptyPy(t *testing.T) string {
	t.Helper()
	b, err := testdata.FS.ReadFile("python/cfgmap_secret/empty.py")
	require.NoError(t, err)
	dst := filepath.Join(t.TempDir(), "empty.py")
	require.NoError(t, os.WriteFile(dst, b, 0o644))
	return dst
}
