//go:build integration

package common_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestTensorflowServingEnv is the Go port of test/tests/test_environments/test_tensorflow_serving_env.sh.
//
// Skipped unless TS_RUNTIME_IMAGE is set (the TF Serving runtime image is
// large and not preloaded in default CI). When set, it deploys the
// vendored half_plus_two SavedModel as a Fission package on both
// poolmgr and newdeploy executors and POSTs an inference request.
//
// The model is a tiny (20K) trained graph that returns
// 0.5 * input + 2 — so [1.0, 2.0, 5.0] → [2.5, 3.0, 4.5].
func TestTensorflowServingEnv(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireTS(t)

	ns := f.NewTestNamespace(t)
	envName := "ts-" + ns.ID
	pkgName := "pkg-ts-" + ns.ID
	fnPM := "fn-ts-pm-" + ns.ID
	fnND := "fn-ts-nd-" + ns.ID

	// TF Serving env needs version 2 (per the bash; it's an env-spec
	// flag the framework didn't expose before, so we use CreateEnvObject
	// for full control). No builder — the model is shipped as a deploy
	// archive.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Period: 5,
	})

	// Pre-built half_plus_two/00000123/{saved_model.pb,variables/...}
	// shipped under testdata/misc/tensorflow_serving/.
	modelZip := framework.ZipTestDataTree(t, "misc/tensorflow_serving", "half_plus_two.zip")
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkgName, Env: envName, Deploy: modelZip,
	})

	// Deploy archives finish "build" instantly (no builder invocation),
	// but the package controller still has to flip status to succeeded.
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnPM, Env: envName, Pkg: pkgName, Entrypoint: "half_plus_two",
	})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnND, Env: envName, Pkg: pkgName, Entrypoint: "half_plus_two",
		ExecutorType: "newdeploy",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnPM, URL: "/" + fnPM, Method: "POST"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnND, URL: "/" + fnND, Method: "POST"})

	body := []byte(`{"instances": [1.0, 2.0, 5.0]}`)
	check := tfPredictionCheck("2.5, 3.0, 4.5")

	gotPM := f.Router(t).PostEventually(t, ctx, "/"+fnPM, "application/json", body, check)
	require.True(t, strings.Contains(gotPM, "2.5"), "poolmgr response missing prediction: %q", gotPM)

	gotND := f.Router(t).PostEventually(t, ctx, "/"+fnND, "application/json", body, check)
	require.True(t, strings.Contains(gotND, "2.5"), "newdeploy response missing prediction: %q", gotND)
}

// tfPredictionCheck returns a ResponseCheck for TF Serving prediction
// payloads of the form `{"predictions": [<floats>]}`. We can't string-
// compare the body directly because TF Serving may format the floats
// slightly differently (2.5 vs 2.50, trailing newline, etc.) so we
// look for the substring `<expected>` somewhere in a 2xx body.
func tfPredictionCheck(expected string) framework.ResponseCheck {
	return func(status int, body string) bool {
		return status >= 200 && status < 300 && strings.Contains(body, expected)
	}
}
