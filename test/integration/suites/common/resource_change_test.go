//go:build integration

package common_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestResourceChange is the Go port of test_fn_update/test_resource_change.sh.
// Verifies that function-level CPU/memory overrides take precedence over the
// env's defaults, both at create-time and after `fn update`.
func TestResourceChange(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-resch-" + ns.ID
	fnName := "fn-resch-" + ns.ID

	// Env defaults: 20/100 CPU, 128/256 MiB.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	// Function overrides env defaults: 40/140 CPU, 256/512 MiB.
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 4,
		MinCPU: 40, MaxCPU: 140, MinMemory: 256, MaxMemory: 512,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	assertResources(t, ctx, ns, fnName, 40, 140, 256, 512)
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))

	// Bump again: 80/200 CPU, 512/768 MiB.
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", codePath,
		"--executortype", "newdeploy", "--minscale", "1", "--maxscale", "4",
		"--mincpu", "80", "--maxcpu", "200", "--minmemory", "512", "--maxmemory", "768")
	assertResources(t, ctx, ns, fnName, 80, 200, 512, 768)
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))
}

// assertResources polls fn.Spec.Resources until requests/limits match the
// expected millicores / MiB. Bash uses jsonpath + `tr -dc '0-9'`; we read
// resource.Quantity strings and strip non-digits the same way.
func assertResources(t *testing.T, ctx context.Context, ns *framework.TestNamespace, fnName string, mincpu, maxcpu, minmem, maxmem int) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fn := ns.GetFunction(t, ctx, fnName)
		req := fn.Spec.Resources.Requests
		lim := fn.Spec.Resources.Limits
		assert.Equalf(c, mincpu, digitsFromQuantity(req.Cpu().String()), "requests.cpu")
		assert.Equalf(c, maxcpu, digitsFromQuantity(lim.Cpu().String()), "limits.cpu")
		assert.Equalf(c, minmem, digitsFromQuantity(req.Memory().String()), "requests.memory")
		assert.Equalf(c, maxmem, digitsFromQuantity(lim.Memory().String()), "limits.memory")
	}, 30*time.Second, 1*time.Second)
}

// digitsFromQuantity strips non-digit chars from a resource.Quantity's
// string form. Mirrors the bash `tr -dc '0-9'` idiom: "100m" → 100,
// "256Mi" → 256.
func digitsFromQuantity(s string) int {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return 0
	}
	n, err := strconv.Atoi(b.String())
	if err != nil {
		return 0
	}
	return n
}
