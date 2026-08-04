//go:build integration

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package serial

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestPoolWarmSurvival pins RFC-0028's warm-pod contract in three phases on
// one environment/function pair. It is the integration-level proof of the
// upgrade gate's warm_pod_survival metric:
//
//  1. An EXECUTOR-side pool-template roll (what a helm upgrade does via a new
//     fetcher image/config) must leave the specialized pod alive, same UID,
//     re-stamped to the new executor instance, and serving throughout.
//  2. An ENVIRONMENT change that touches the pod template must still recycle
//     it — the discriminator keys on a hash of the template inputs, and this
//     is the regression guard for the path that used to be processRS's only
//     reading. (TerminationGracePeriod is a template input; poolsize is not
//     and deliberately would NOT recycle.)
//  3. Deleting the environment must drain the specialized pods entirely.
//
// Serial: it restarts the shared executor (adopt runs before readiness, so a
// completed rollout means the adopt pass finished).
func TestPoolWarmSurvival(t *testing.T) {
	// Budget must exceed the sum of the phase Eventually budgets (5+3+2+4+4m)
	// plus warm-up, or a slow-but-passing run degrades into ctx-deadline
	// errors inside the conditions instead of a clean per-phase timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	// Registered BEFORE the SetExecutorEnv restores (cleanups run LIFO, so it
	// executes AFTER them): the restores fire two back-to-back un-awaited
	// executor rollouts, and without this wait they land in whichever serial
	// test runs next.
	t.Cleanup(func() { f.WaitForExecutorSettled(t, 3*time.Minute) })

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pws-" + ns.ID
	fnName := "fn-pws-" + ns.ID
	route := "/" + fnName

	// Short grace so phase 2/3 recycles are observable quickly.
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image, GracePeriod: 5})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath, ExecutorType: "poolmgr",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: route, Method: "GET"})

	// Warm it: the first invocation specializes a pool pod.
	f.Router(t).GetEventually(t, ctx, route, framework.BodyContains("hello"))
	podsBefore, err := ns.SpecializedFunctionPods(ctx, fnName)
	require.NoError(t, err)
	require.NotEmpty(t, podsBefore, "warm-up must have specialized a pod")
	uidBefore := podsBefore[0].UID
	instBefore := podsBefore[0].Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
	require.NotEmpty(t, instBefore, "a specialized pod carries the claiming executor's instance annotation")

	// ---- Phase 1: executor-side template roll -> the pod SURVIVES ----
	//
	// FETCHER_MINMEM reaches every pool template through the fetcher config
	// the executor reads at startup, so this restart regenerates the pool
	// spec exactly the way a helm upgrade's fetcher-image change does:
	// new RS, old RS to zero — mechanism B's trigger. SetExecutorEnv also
	// forces Recreate for the roll, keeping the test independent of
	// chart-default sequencing; adoption is pinned on explicitly.
	_, restoreAdopt := f.SetExecutorEnv(t, ctx, "ADOPT_EXISTING_RESOURCES", "true")
	t.Cleanup(restoreAdopt)
	gen, restoreMem := f.SetExecutorEnv(t, ctx, "FETCHER_MINMEM", "17Mi")
	t.Cleanup(restoreMem)
	f.WaitForExecutorRollout(t, ctx, gen, 5*time.Minute)

	// The pool template genuinely rolled (the whole point of the trigger):
	// its fetcher container must now carry the bumped memory request.
	ns.WaitForPoolDeployment(t, ctx, envName, func(d *appsv1.Deployment) bool {
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name != "fetcher" {
				continue
			}
			if q, ok := c.Resources.Requests["memory"]; ok && q.String() == "17Mi" {
				return true
			}
		}
		return false
	}, "pool template must carry the new fetcher config (proves the template roll fired)", 3*time.Minute)

	// Survival, the headline: the SAME pod (by UID), re-stamped to the NEW
	// instance. Look the pod up by uidBefore rather than trusting list order:
	// a second specialized pod (a racing router retry) must not make these
	// assertions compare the wrong one.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, lerr := ns.SpecializedFunctionPods(ctx, fnName)
		if !assert.NoError(c, lerr) {
			return
		}
		var survived *corev1.Pod
		for i := range pods {
			if pods[i].UID == uidBefore {
				survived = &pods[i]
				break
			}
		}
		if !assert.NotNil(c, survived,
			"the specialized pod must survive the executor roll IN PLACE — its UID vanishing means it was killed and recreated") {
			return
		}
		inst := survived.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
		// Both halves, deliberately: NotEqual alone is satisfied by an EMPTY
		// annotation — the exact never-re-stamped failure mode.
		assert.NotEmpty(c, inst, "the surviving pod must carry an instance annotation at all")
		assert.NotEqual(c, instBefore, inst,
			"the surviving pod must be re-stamped by the NEW executor's adopt pass")
	}, 2*time.Minute, 2*time.Second)

	// And it serves.
	for range 5 {
		f.Router(t).GetEventually(t, ctx, route, framework.BodyContains("hello"))
	}

	// ---- Phase 2: environment spec change -> the pod IS recycled ----
	ns.UpdateEnvSpec(t, ctx, envName, func(env *fv1.Environment) {
		env.Spec.TerminationGracePeriod = new(env.Spec.EffectiveTerminationGracePeriod() + 1)
	})
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, lerr := ns.SpecializedFunctionPods(ctx, fnName)
		if !assert.NoError(c, lerr) {
			return
		}
		for _, p := range pods {
			if p.DeletionTimestamp != nil {
				continue // already on its way out
			}
			assert.NotEqualf(c, uidBefore, p.UID,
				"an environment change must recycle the old-template pod (still live: %s)", p.Name)
		}
	}, 4*time.Minute, 3*time.Second)
	// The function still serves — on a fresh pod.
	f.Router(t).GetEventually(t, ctx, route, framework.BodyContains("hello"))

	// ---- Phase 3: environment delete -> specialized pods drain ----
	require.NoError(t,
		f.FissionClient().CoreV1().Environments(ns.Name).Delete(ctx, envName, metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, lerr := ns.SpecializedFunctionPods(ctx, fnName)
		if !assert.NoError(c, lerr) {
			return
		}
		live := 0
		for _, p := range pods {
			if p.DeletionTimestamp == nil {
				live++
			}
		}
		assert.Zero(c, live, "env delete must reap every specialized pod")
	}, 4*time.Minute, 3*time.Second)
}
