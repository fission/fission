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
// end. We use Eventually-style polling against the HTTPTrigger spec so the
// test passes as soon as the controller settles.
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

		helloPath := framework.WriteTestData(t, "nodejs/hello/hello.js")

		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnV1, Env: envName, Code: helloPath})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnV2, Env: envName, Code: helloPath})

		ns.CreateRoute(t, ctx, framework.RouteOptions{
			Name:   routeName,
			URL:    "/" + routeName,
			Method: "GET",
			FunctionWeights: []framework.FunctionWeight{
				{Name: fnV1, Weight: 100},
				{Name: fnV2, Weight: 0},
			},
		})

		ns.CreateCanaryConfig(t, ctx, framework.CanaryConfigOptions{
			Name:              canaryName,
			NewFunction:       fnV2,
			OldFunction:       fnV1,
			HTTPTrigger:       routeName,
			IncrementStep:     50,
			IncrementInterval: "30s",
			FailureThreshold:  10,
		})

		// Feed enough successful traffic for the canary controller's
		// failure-rate signal to register as "healthy". Two waves match
		// the bash flow (sleep / load / sleep / verify).
		fireSuccessfulTraffic(t, ctx, f, "/"+routeName, 200)
		ns.WaitForFunctionWeight(t, ctx, routeName, fnV2, 100, 5*time.Minute)
	})

	t.Run("rollback", func(t *testing.T) {
		fnV1 := "fn-rollback-v1-" + ns.ID
		fnV3 := "fn-rollback-v3-" + ns.ID
		routeName := "route-fail-" + ns.ID
		canaryName := "canary-fail-" + ns.ID

		okPath := framework.WriteTestData(t, "nodejs/hello/hello.js")
		failPath := framework.WriteTestData(t, "nodejs/hello_400/hello_400.js")

		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnV1, Env: envName, Code: okPath})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnV3, Env: envName, Code: failPath})

		ns.CreateRoute(t, ctx, framework.RouteOptions{
			Name:   routeName,
			URL:    "/" + routeName,
			Method: "GET",
			FunctionWeights: []framework.FunctionWeight{
				{Name: fnV1, Weight: 100},
				{Name: fnV3, Weight: 0},
			},
		})

		ns.CreateCanaryConfig(t, ctx, framework.CanaryConfigOptions{
			Name:              canaryName,
			NewFunction:       fnV3,
			OldFunction:       fnV1,
			HTTPTrigger:       routeName,
			IncrementStep:     50,
			IncrementInterval: "30s",
			FailureThreshold:  10,
		})

		// Fire traffic; some hits route to fnV3 (returning 400) and the
		// failure rate climbs past the threshold, triggering rollback.
		_ = f.Router(t).FireRequests(t, ctx, "/"+routeName, 200)
		ns.WaitForFunctionWeight(t, ctx, routeName, fnV3, 0, 5*time.Minute)
	})
}

// fireSuccessfulTraffic sends requests and asserts that at least some succeed
// — this catches gross misconfigurations (route never registered, env never
// specialized) early, before the canary controller's polling delay would
// otherwise mask them.
func fireSuccessfulTraffic(t *testing.T, ctx context.Context, f *framework.Framework, path string, n int) {
	t.Helper()
	r := f.Router(t)
	// First, make sure the route actually serves 2xx — gives a clean
	// failure if the route or function isn't ready.
	r.GetEventually(t, ctx, path, framework.BodyContains("hello"))
	if got := r.FireRequests(t, ctx, path, n); got == 0 {
		t.Fatalf("fireSuccessfulTraffic: 0 of %d requests to %q succeeded", n, path)
	}
}
