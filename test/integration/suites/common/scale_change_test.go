//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestScaleChange is the Go port of test_fn_update/test_scale_change.sh.
// Creates a newdeploy fn with min/max=1/4, verifies it serves traffic,
// updates to min/max=2/6 with targetcpu=60, then asserts the spec reflects
// the new values via the typed clientset.
func TestScaleChange(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-scale-" + ns.ID
	fnName := "fn-scale-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 4,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))

	// Update scale + targetcpu via raw CLI (no high-level helper for fn update).
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", codePath,
		"--minscale", "2", "--maxscale", "6", "--targetcpu", "60",
		"--executortype", "newdeploy",
		"--mincpu", "20", "--maxcpu", "100", "--minmemory", "128", "--maxmemory", "256")

	// Spec reflects the new scale bounds. The bash version reads
	// `hpaMetrics[0].target.averageUtilization` for targetCPU; the typed
	// field is Metrics[0].Resource.Target.AverageUtilization.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fn := ns.GetFunction(t, ctx, fnName)
		s := fn.Spec.InvokeStrategy.ExecutionStrategy
		assert.Equalf(c, 2, s.MinScale, "MinScale should be 2 (got %d)", s.MinScale)
		assert.Equalf(c, 6, s.MaxScale, "MaxScale should be 6 (got %d)", s.MaxScale)
		if assert.NotEmptyf(c, s.Metrics, "expected at least one HPA metric") {
			r := s.Metrics[0].Resource
			if assert.NotNilf(c, r, "Metrics[0].Resource is nil") &&
				assert.NotNilf(c, r.Target.AverageUtilization, "AverageUtilization is nil") {
				assert.Equalf(c, int32(60), *r.Target.AverageUtilization,
					"target averageUtilization should be 60 (got %d)", *r.Target.AverageUtilization)
			}
		}
	}, 30*time.Second, 1*time.Second)

	// Function still serves traffic after update.
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))
}
