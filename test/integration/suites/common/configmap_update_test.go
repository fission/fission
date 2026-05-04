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

// TestConfigMapUpdate is the Go port of test_fn_update/test_configmap_update.sh.
// Creates a newdeploy function that reads /configs/default/<cfgmap>/TEST_KEY,
// then updates both the function code and its mounted configmap reference,
// and verifies the new value is served. The bash version achieves this via
// `fission spec init/apply` + sed-rewriting the generated yaml — the Go
// version uses `fn update --configmap` directly (the CLI fully replaces
// Spec.ConfigMaps on update).
func TestConfigMapUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-cfgmap-" + ns.ID
	fnName := "fn-cfgmap-" + ns.ID
	oldCfg := "old-cfg-" + ns.ID
	newCfg := "new-cfg-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	ns.CreateConfigMap(t, ctx, oldCfg, map[string]string{"TEST_KEY": "TESTVALUE"})
	codeOld := writeCfgmapPy(t, oldCfg)
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codeOld,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 4,
		ConfigMaps: []string{oldCfg},
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("TESTVALUE"))
	require.Contains(t, body, "TESTVALUE")

	// Swap to a new configmap with a new value, and update both the
	// code (which hardcodes the configmap name in the path) and the
	// function's ConfigMaps reference in one fn-update CLI call.
	ns.CreateConfigMap(t, ctx, newCfg, map[string]string{"TEST_KEY": "TESTVALUE_NEW"})
	codeNew := writeCfgmapPy(t, newCfg)
	ns.CLI(t, ctx, "fn", "update", "--name", fnName,
		"--code", codeNew, "--configmap", newCfg)

	body = f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("TESTVALUE_NEW"))
	require.Contains(t, body, "TESTVALUE_NEW")
}

// writeCfgmapPy materializes the vendored cfgmap.py.template under
// t.TempDir with FN_CFGMAP substituted to the given configmap name.
func writeCfgmapPy(t *testing.T, cfgmap string) string {
	t.Helper()
	tpl, err := testdata.FS.ReadFile("python/cfgmap_secret/cfgmap.py.template")
	require.NoError(t, err)
	body := strings.ReplaceAll(string(tpl), "{{ FN_CFGMAP }}", cfgmap)
	dst := filepath.Join(t.TempDir(), "cfgmap.py")
	require.NoError(t, os.WriteFile(dst, []byte(body), 0o644))
	return dst
}
