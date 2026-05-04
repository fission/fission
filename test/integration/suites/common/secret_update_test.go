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

// TestSecretUpdate is the Go port of test_fn_update/test_secret_update.sh.
// Symmetric to TestConfigMapUpdate but for Secrets — the function reads
// /secrets/default/<secret>/TEST_KEY, then both the function code and
// mounted Secret reference are swapped.
func TestSecretUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-secret-" + ns.ID
	fnName := "fn-secret-" + ns.ID
	oldSec := "old-sec-" + ns.ID
	newSec := "new-sec-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	ns.CreateSecret(t, ctx, oldSec, map[string]string{"TEST_KEY": "TESTVALUE"})
	codeOld := writeSecretPy(t, oldSec)
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codeOld,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 4,
		Secrets: []string{oldSec},
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("TESTVALUE"))
	require.Contains(t, body, "TESTVALUE")

	ns.CreateSecret(t, ctx, newSec, map[string]string{"TEST_KEY": "TESTVALUE_NEW"})
	codeNew := writeSecretPy(t, newSec)
	ns.CLI(t, ctx, "fn", "update", "--name", fnName,
		"--code", codeNew, "--secret", newSec)

	body = f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("TESTVALUE_NEW"))
	require.Contains(t, body, "TESTVALUE_NEW")
}

// writeSecretPy materializes the vendored secret.py.template under
// t.TempDir with FN_SECRET substituted to the given secret name.
func writeSecretPy(t *testing.T, secret string) string {
	t.Helper()
	tpl, err := testdata.FS.ReadFile("python/cfgmap_secret/secret.py.template")
	require.NoError(t, err)
	body := strings.ReplaceAll(string(tpl), "{{ FN_SECRET }}", secret)
	dst := filepath.Join(t.TempDir(), "secret.py")
	require.NoError(t, os.WriteFile(dst, []byte(body), 0o644))
	return dst
}
