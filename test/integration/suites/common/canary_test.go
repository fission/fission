//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/fission/fission/test/integration/framework"
)

// TestCanary is the Go port of test/tests/test_canary.sh. It exercises the
// CanaryConfig controller's two terminal outcomes:
//
//   - "success": when the new function returns 2xx, the controller increments
//     its weight by `--increment-step` every `--increment-interval` until it
//     reaches 100.
//
//   - "rollback": when the new function exceeds the failure threshold, the
//     controller flips traffic back to 100% old / 0% new.
//
// The bash test fixed-sleeps for 2 minutes per scenario and asserts at the
// end. We poll the HTTPTrigger spec while a goroutine fires sustained
// background traffic — the canary controller measures failure rate per
// tick, so the test must keep traffic flowing the whole time.
func TestCanary(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-canary-" + ns.ID

	// Short graceperiod helps stale routes flip cleanly between v1 and v2.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name:        envName,
		Image:       image,
		GracePeriod: 1,
	})

	t.Run("success", func(t *testing.T) {
		fnV1 := "fn-v1-" + ns.ID
		fnV2 := "fn-v2-" + ns.ID
		routeName := "route-succ-" + ns.ID
		canaryName := "canary-succ-" + ns.ID
		routePath := "/" + routeName

		helloPath := framework.WriteTestData(t, "nodejs/hello/hello.js")

		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnV1, Env: envName, Code: helloPath})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnV2, Env: envName, Code: helloPath})

		ns.CreateRoute(t, ctx, framework.RouteOptions{
			Name:   routeName,
			URL:    routePath,
			Method: "GET",
			FunctionWeights: []framework.FunctionWeight{
				{Name: fnV1, Weight: 100},
				{Name: fnV2, Weight: 0},
			},
		})

		// Make sure the route actually serves 2xx before the canary kicks
		// in — gives a clean failure if the route or function isn't ready.
		f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))

		ns.CreateCanaryConfig(t, ctx, framework.CanaryConfigOptions{
			Name:              canaryName,
			NewFunction:       fnV2,
			OldFunction:       fnV1,
			HTTPTrigger:       routeName,
			IncrementStep:     50,
			IncrementInterval: "30s",
			FailureThreshold:  10,
		})

		startBackgroundLoad(t, ctx, f, routePath)
		ns.WaitForFunctionWeight(t, ctx, routeName, fnV2, 100, 5*time.Minute)
	})

	t.Run("rollback", func(t *testing.T) {
		fnV1 := "fn-rollback-v1-" + ns.ID
		fnV3 := "fn-rollback-v3-" + ns.ID
		routeName := "route-fail-" + ns.ID
		canaryName := "canary-fail-" + ns.ID
		routePath := "/" + routeName

		okPath := framework.WriteTestData(t, "nodejs/hello/hello.js")
		failPath := framework.WriteTestData(t, "nodejs/hello_400/hello_400.js")

		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnV1, Env: envName, Code: okPath})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnV3, Env: envName, Code: failPath})

		ns.CreateRoute(t, ctx, framework.RouteOptions{
			Name:   routeName,
			URL:    routePath,
			Method: "GET",
			FunctionWeights: []framework.FunctionWeight{
				{Name: fnV1, Weight: 100},
				{Name: fnV3, Weight: 0},
			},
		})

		f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))

		ns.CreateCanaryConfig(t, ctx, framework.CanaryConfigOptions{
			Name:              canaryName,
			NewFunction:       fnV3,
			OldFunction:       fnV1,
			HTTPTrigger:       routeName,
			IncrementStep:     50,
			IncrementInterval: "30s",
			FailureThreshold:  10,
		})

		startBackgroundLoad(t, ctx, f, routePath)

		// The failure threshold is measured *during a tick where v3 actually
		// receives traffic*. With initial weight v3=0, the controller has to
		// first increment to a non-zero weight (e.g. 50) before failures can
		// register. Wait for that first increment to confirm the canary is
		// alive — otherwise a static v3=0 (broken controller) would falsely
		// pass the rollback check below.
		ns.WaitForFunctionWeightAtLeast(t, ctx, routeName, fnV3, 1, 2*time.Minute)

		// Now wait for the controller to observe failures and roll back.
		ns.WaitForFunctionWeight(t, ctx, routeName, fnV3, 0, 5*time.Minute)
	})
}

// startBackgroundLoad spawns a goroutine that fires GETs to `path` until the
// surrounding test context cancels (or t.Cleanup fires). The canary controller
// makes its weight-shift decisions per tick using the failure rate observed
// *during that tick*, so the test must keep traffic flowing throughout the
// poll, not just up front.
func startBackgroundLoad(t *testing.T, ctx context.Context, f *framework.Framework, path string) {
	t.Helper()
	loadCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go f.Router(t).LoadLoop(loadCtx, path)
}
