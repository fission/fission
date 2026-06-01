// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package serial_test holds integration tests that mutate cluster-wide
// control-plane state and therefore cannot run alongside the parallel `common`
// suite. They run single-threaded (`go test -p 1`) after it. The first such
// test restarts the shared executor to exercise its startup-only adopt path.
package serial_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestAdoptExistingResources exercises every executor type's
// AdoptExistingResources path — newdeploy, container, and poolmgr — which runs
// only at executor startup when ADOPT_EXISTING_RESOURCES=true and otherwise has
// no end-to-end coverage.
//
// On restart the executor picks a fresh random instance ID. AdoptExistingResources
// finds each pre-existing backing Deployment and updates it *in place* (same
// object), re-stamping the executorInstanceId annotation to the new ID — so the
// orphan reaper (which deletes objects whose annotation != the current ID)
// leaves it alone instead of deleting it and forcing a cold recreate. We prove
// adoption by asserting the Deployment keeps its UID and creationTimestamp
// (not recreated) while its instance-ID annotation flips to a new value.
//
//   - newdeploy / container: per-function Deployment (createOrGetDeployment /
//     container deployment adopt branch).
//   - poolmgr: the env-scoped warm-pool Deployment (createPoolDeployment adopt
//     branch); specialized-pod re-adoption is exercised behaviourally by the
//     function continuing to serve after the restart with no cold recreate.
//
// This test lives in the serial suite because restarting the single,
// cluster-wide executor is incompatible with the parallel common suite.
func TestAdoptExistingResources(t *testing.T) {
	// Deliberately NOT t.Parallel(): restarts the shared executor (see package doc).

	// 15m budget for the whole flow (3 functions + a full executor rollout +
	// re-stamp waits). Kept below the `go test -timeout` in CI so this context
	// deadline — with a clear failure message — fires before the hard timeout.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	pyImage := f.Images().RequirePython(t)
	ctrImage := f.Images().RequireContainer(t)

	const port = 8888
	ns := f.NewTestNamespace(t)
	envName := "adopt-py-" + ns.ID
	pmFn := "adopt-pm-" + ns.ID
	ndFn := "adopt-nd-" + ns.ID
	ctrFn := "adopt-ctr-" + ns.ID
	ctrCM := "adopt-ctr-port-" + ns.ID

	// A python env with a non-zero pool size so poolmgr maintains a warm-pool
	// Deployment to adopt.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: pyImage, Poolsize: 2,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "python/hello/hello.py")

	// poolmgr (default executor) — warm-pool path.
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: pmFn, Env: envName, Code: codePath,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: pmFn, URL: "/" + pmFn, Method: "GET"})

	// newdeploy — per-function Deployment path. MinScale 1 keeps the Deployment
	// up while idle so it's there to adopt.
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: ndFn, Env: envName, Code: codePath,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 2,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: ndFn, URL: "/" + ndFn, Method: "GET"})

	// container — user image, PORT injected via configmap (as in TestBackendContainer).
	ns.CreateConfigMap(t, ctx, ctrCM, map[string]string{"PORT": strconv.Itoa(port)})
	ns.CreateContainerFunction(t, ctx, framework.ContainerFunctionOptions{
		Name: ctrFn, Image: ctrImage, Port: port, ConfigMaps: []string{ctrCM},
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: ctrFn, URL: "/" + ctrFn, Method: "GET"})

	is2xx := func(status int, _ string) bool {
		return status >= http.StatusOK && status < http.StatusMultipleChoices
	}
	r := f.Router(t)
	// Drive each function so its backing objects exist — and, for poolmgr, a pod
	// is specialized — before we restart the executor.
	r.GetEventually(t, ctx, "/"+pmFn, framework.BodyContains("world"))
	r.GetEventually(t, ctx, "/"+ndFn, framework.BodyContains("world"))
	r.GetEventually(t, ctx, "/"+ctrFn, is2xx)

	instIDOf := func(d *appsv1.Deployment) string { return d.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] }
	anyDeployment := func(*appsv1.Deployment) bool { return true }

	// Snapshot each backing Deployment and the executor instance ID that owns it.
	ndBefore := ns.WaitForFunctionDeployment(t, ctx, ndFn, anyDeployment,
		"newdeploy Deployment exists before adopt", 90*time.Second)
	ctrBefore := ns.WaitForFunctionDeployment(t, ctx, ctrFn, anyDeployment,
		"container Deployment exists before adopt", 90*time.Second)
	poolBefore := ns.WaitForPoolDeployment(t, ctx, envName, anyDeployment,
		"poolmgr pool Deployment exists before adopt", 90*time.Second)

	for _, d := range []*appsv1.Deployment{ndBefore, ctrBefore, poolBefore} {
		require.NotEmptyf(t, instIDOf(d),
			"deployment %q should carry an executor instance ID before adopt", d.Name)
	}
	oldND, oldCTR, oldPool := instIDOf(ndBefore), instIDOf(ctrBefore), instIDOf(poolBefore)

	// Enable adopt and restart the executor; wait for the new pod to be serving
	// (which means its adopt pass has run).
	gen, restore := f.SetExecutorEnv(t, ctx, "ADOPT_EXISTING_RESOURCES", "true")
	t.Cleanup(restore)
	f.WaitForExecutorRollout(t, ctx, gen, 5*time.Minute)

	// reStamped matches a Deployment whose instance-ID annotation has flipped to
	// a new, non-empty value — i.e. the new executor adopted it.
	reStamped := func(oldID string) func(*appsv1.Deployment) bool {
		return func(d *appsv1.Deployment) bool {
			id := instIDOf(d)
			return id != "" && id != oldID
		}
	}
	// assertAdopted: same object (UID + creationTimestamp), annotation re-stamped
	// — adopted in place, not deleted-and-recreated.
	assertAdopted := func(t *testing.T, kind string, before, after *appsv1.Deployment, oldID string) {
		t.Helper()
		require.Equalf(t, before.UID, after.UID,
			"%s Deployment %q must be adopted in place (same UID), not recreated", kind, before.Name)
		require.Equalf(t, before.CreationTimestamp, after.CreationTimestamp,
			"%s Deployment %q must keep its creationTimestamp (adopted, not recreated)", kind, before.Name)
		require.NotEqualf(t, oldID, instIDOf(after),
			"%s Deployment %q instance-ID annotation should be re-stamped to the new executor", kind, before.Name)
	}

	ndAfter := ns.WaitForFunctionDeployment(t, ctx, ndFn, reStamped(oldND),
		"newdeploy Deployment re-stamped with the new executor instance ID", 3*time.Minute)
	assertAdopted(t, "newdeploy", ndBefore, ndAfter, oldND)

	ctrAfter := ns.WaitForFunctionDeployment(t, ctx, ctrFn, reStamped(oldCTR),
		"container Deployment re-stamped with the new executor instance ID", 3*time.Minute)
	assertAdopted(t, "container", ctrBefore, ctrAfter, oldCTR)

	poolAfter := ns.WaitForPoolDeployment(t, ctx, envName, reStamped(oldPool),
		"poolmgr pool Deployment re-stamped with the new executor instance ID", 3*time.Minute)
	assertAdopted(t, "poolmgr", poolBefore, poolAfter, oldPool)

	// All three functions still serve after the restart: the adopted pods kept
	// running without a cold recreate (poolmgr's specialized pod was re-added to
	// the function-service cache; the newdeploy/container Deployments were untouched).
	r.GetEventually(t, ctx, "/"+pmFn, framework.BodyContains("world"))
	r.GetEventually(t, ctx, "/"+ndFn, framework.BodyContains("world"))
	r.GetEventually(t, ctx, "/"+ctrFn, is2xx)
}
