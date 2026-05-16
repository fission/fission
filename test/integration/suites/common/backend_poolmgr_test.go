//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestBackendPoolmgr is the Go port of test_backend_poolmgr.sh — a
// simple smoke that the poolmgr executor backend serves a hello function.
// (TestNodeHelloHTTP already covers this implicitly with default poolmgr;
// here we keep it explicit to mirror the bash CI surface.)
func TestBackendPoolmgr(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pmbk-" + ns.ID
	fnName := "fn-pm-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath, ExecutorType: "poolmgr",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")

	// Once a request has been served end-to-end the controllers should have
	// populated every Ready condition along the dispatch chain:
	//   - the buildermgr marked the backing Package as Ready (BuildSucceeded)
	//   - the poolmgr executor specialized a pool pod and marked the
	//     Function and the Environment as Ready (Specialized / PoolReady)
	//   - the router admitted the HTTPTrigger's route (RouteAdmitted)
	// 30s is generous — by this point every writer has had ample time.
	pkgName := ns.FunctionPackageName(t, ctx, fnName)
	ns.WaitForPackageConditionTrue(t, ctx, pkgName, fv1.PackageConditionBuildSucceeded, 30*time.Second)
	ns.WaitForPackageConditionTrue(t, ctx, pkgName, fv1.PackageConditionReady, 30*time.Second)
	ns.WaitForFunctionConditionReady(t, ctx, fnName, fv1.FunctionConditionReady, 30*time.Second)
	ns.WaitForEnvironmentConditionReady(t, ctx, envName, fv1.EnvironmentConditionReady, 30*time.Second)
}
