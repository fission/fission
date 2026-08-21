// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

func TestSetCanaryConfigConditions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status       string
		wantProgress metav1.ConditionStatus
		wantReady    metav1.ConditionStatus
	}{
		{fv1.CanaryConfigStatusPending, metav1.ConditionTrue, metav1.ConditionFalse},
		{fv1.CanaryConfigStatusSucceeded, metav1.ConditionFalse, metav1.ConditionTrue},
		{fv1.CanaryConfigStatusFailed, metav1.ConditionFalse, metav1.ConditionFalse},
		{fv1.CanaryConfigStatusAborted, metav1.ConditionFalse, metav1.ConditionFalse},
		{"something-unknown", metav1.ConditionUnknown, metav1.ConditionUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			var s fv1.CanaryConfigStatus
			require.True(t, setCanaryConfigConditions(&s, tt.status, 7, ""), "first set reports a change")
			assert.False(t, setCanaryConfigConditions(&s, tt.status, 7, ""), "re-setting the same conditions reports no change")

			prog := apimeta.FindStatusCondition(s.Conditions, fv1.CanaryConfigConditionProgressing)
			ready := apimeta.FindStatusCondition(s.Conditions, fv1.CanaryConfigConditionReady)
			require.NotNil(t, prog)
			require.NotNil(t, ready)
			assert.Equal(t, tt.wantProgress, prog.Status)
			assert.Equal(t, tt.wantReady, ready.Status)
			assert.Equal(t, int64(7), prog.ObservedGeneration)
		})
	}
}

func TestGetEnvValue(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "bar", getEnvValue("FOO=bar"))
}

func TestGetFunctionQueryLabels(t *testing.T) {
	t.Parallel()
	c := &PrometheusApiClient{logger: logr.Discard()}
	got := c.getFunctionQueryLabels("fn", "", "ns", "/p", "GET")
	assert.Equal(t, `function_name="fn",function_namespace="ns",path="/p",method="GET"`, got)
}

func TestGetFunctionQueryLabelsWithVersion(t *testing.T) {
	t.Parallel()
	c := &PrometheusApiClient{logger: logr.Discard()}
	got := c.getFunctionQueryLabels("fn", "fn-v3", "ns", "/p", "GET")
	assert.Equal(t, `function_name="fn",function_namespace="ns",path="/p",method="GET",function_version="fn-v3"`, got)
}

func TestMakePrometheusClient(t *testing.T) {
	t.Parallel()
	c, err := MakePrometheusClient(logr.Discard(), "http://prometheus.monitoring:9090")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// fakeFailureClient is a deterministic failurePercentageGetter for tests. It
// also records every call's (funcName, funcVersion, funcNs) so alias-mode
// tests can assert the shim passes alias.Spec.FunctionName (not the version
// name) as funcName, per RFC-0025 plan-review blocker #2.
type fakeFailureClient struct {
	pct   float64
	err   error
	calls []fakeFailureCall
}

type fakeFailureCall struct {
	funcName, funcVersion, funcNs string
}

func (f *fakeFailureClient) GetFunctionFailurePercentage(_ context.Context, q failureQuery) (float64, error) {
	f.calls = append(f.calls, fakeFailureCall{q.Function, q.Version, q.Namespace})
	return f.pct, f.err
}

func canaryFixtures(weights map[string]int, increment int) (*fv1.HTTPTrigger, *fv1.CanaryConfig) {
	trigger := &fv1.HTTPTrigger{Name: "trig", Namespace: "default"}
	trigger.Spec.FunctionReference.Type = fv1.FunctionReferenceTypeFunctionWeights
	trigger.Spec.FunctionReference.FunctionWeights = weights
	cc := &fv1.CanaryConfig{
		Name: "cc", Namespace: "default", Generation: 1,
		Spec: fv1.CanaryConfigSpec{
			Trigger: "trig", NewFunction: "new", OldFunction: "old",
			WeightIncrement:         increment,
			WeightIncrementDuration: "1m",
			FailureThreshold:        10,
		},
		Status: fv1.CanaryConfigStatus{Status: fv1.CanaryConfigStatusPending},
	}
	return trigger, cc
}

// newTestEnv wires a canaryConfigMgr and a CanaryConfigReconciler onto a shared
// controller-runtime fake client seeded with objs.
func newTestEnv(prom failurePercentageGetter, objs ...client.Object) (*canaryConfigMgr, *CanaryConfigReconciler, client.Client) {
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithStatusSubresource(&fv1.CanaryConfig{}).
		Build()
	mgr := &canaryConfigMgr{logger: logr.Discard(), client: c, apiReader: c, promClient: prom}
	r := &CanaryConfigReconciler{logger: logr.Discard(), client: c, mgr: mgr}
	return mgr, r, c
}

func getTrigger(t *testing.T, c client.Client) *fv1.HTTPTrigger {
	t.Helper()
	got := &fv1.HTTPTrigger{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "trig"}, got))
	return got
}

func getConfig(t *testing.T, c client.Client) *fv1.CanaryConfig {
	t.Helper()
	got := &fv1.CanaryConfig{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "cc"}, got))
	return got
}

func TestRollForward(t *testing.T) {
	t.Run("increments weights without completing", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 30)
		mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc)

		done, err := mgr.rollForward(t.Context(), cc, trigger)
		require.NoError(t, err)
		assert.False(t, done)

		got := getTrigger(t, c)
		assert.Equal(t, 30, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 70, got.Spec.FunctionReference.FunctionWeights["old"])
	})

	t.Run("completes when the increment reaches 100", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 80, "old": 20}, 30)
		mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc)

		done, err := mgr.rollForward(t.Context(), cc, trigger)
		require.NoError(t, err)
		assert.True(t, done)

		got := getTrigger(t, c)
		assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["old"])
	})

	t.Run("increment >= 100 from zero shifts all traffic but is not done", func(t *testing.T) {
		// The step that INTRODUCES first traffic must never also be the terminal
		// one: step() skips the failure evaluation at weight 0, so done here
		// would mean Succeeded with zero observed evaluation windows.
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 100)
		mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc)

		done, err := mgr.rollForward(t.Context(), cc, trigger)
		require.NoError(t, err)
		assert.False(t, done, "a step from weight 0 must not report done — the next tick must evaluate first")

		got := getTrigger(t, c)
		assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["old"])
	})
}

func TestRollbackWeights(t *testing.T) {
	trigger, cc := canaryFixtures(map[string]int{"new": 50, "old": 50}, 30)
	mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc)

	require.NoError(t, mgr.rollbackWeights(t.Context(), cc, trigger))

	got := getTrigger(t, c)
	assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["new"])
	assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["old"])
	// rollbackWeights must not touch the canary status — that is the
	// reconciler's job.
	assert.Equal(t, fv1.CanaryConfigStatusPending, getConfig(t, c).Status.Status)
}

func TestStep(t *testing.T) {
	t.Run("trigger missing requeues", func(t *testing.T) {
		_, cc := canaryFixtures(nil, 30)
		mgr, _, _ := newTestEnv(&fakeFailureClient{}, cc) // no trigger seeded

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
	})

	t.Run("no traffic requeues without changing weights", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		mgr, _, c := newTestEnv(&fakeFailureClient{pct: -1}, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
		assert.Equal(t, 30, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})

	t.Run("failure query error requeues", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		mgr, _, _ := newTestEnv(&fakeFailureClient{err: assert.AnError}, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
	})

	t.Run("threshold crossed rolls back and reports Failed", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		mgr, _, c := newTestEnv(&fakeFailureClient{pct: 50}, trigger, cc) // > threshold 10

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)

		got := getTrigger(t, c)
		assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["old"])
	})

	t.Run("non-weighted trigger requeues without panicking", func(t *testing.T) {
		trigger, cc := canaryFixtures(nil, 30) // nil FunctionWeights map
		trigger.Spec.FunctionReference.Type = fv1.FunctionReferenceTypeFunctionName
		mgr, _, _ := newTestEnv(&fakeFailureClient{}, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
	})

	t.Run("weighted trigger with nil weights map requeues without panicking", func(t *testing.T) {
		trigger, cc := canaryFixtures(nil, 30) // type is function-weights, map nil
		mgr, _, _ := newTestEnv(&fakeFailureClient{}, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
	})

	t.Run("under threshold increments and requeues", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		mgr, _, c := newTestEnv(&fakeFailureClient{pct: 5}, trigger, cc) // < threshold 10

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
		assert.Equal(t, 60, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})

	t.Run("reaching 100 reports Succeeded", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 80, "old": 20}, 30)
		mgr, _, c := newTestEnv(&fakeFailureClient{pct: 5}, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusSucceeded, out.terminalStatus)

		got := getTrigger(t, c)
		assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["old"])
	})

	t.Run("increment >= 100 with failing new function never reports Succeeded", func(t *testing.T) {
		// Regression for the single-tick promote bug: with WeightIncrement >= 100
		// the first tick used to shift 0 -> 100 AND report Succeeded — a terminal
		// status the reconciler never revisits — without ever evaluating the new
		// function. Now the introducing tick stays Pending and the next tick's
		// evaluation rolls a failing function back.
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 100)
		prom := &fakeFailureClient{pct: 50} // > threshold 10
		mgr, _, c := newTestEnv(prom, trigger, cc)

		// Tick 1: introduces traffic, no evaluation possible yet, not terminal.
		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
		assert.Empty(t, prom.calls, "at weight 0 there is nothing to evaluate")
		assert.Equal(t, 100, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])

		// Tick 2: first evaluation window completed — threshold crossed, roll back.
		out, err = mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		require.Len(t, prom.calls, 1)

		got := getTrigger(t, c)
		assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["old"])
	})

	t.Run("increment >= 100 with healthy new function succeeds only after an evaluated window", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 100)
		prom := &fakeFailureClient{pct: 5} // < threshold 10
		mgr, _, c := newTestEnv(prom, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out, "introducing tick must not be terminal")

		out, err = mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusSucceeded, out.terminalStatus)
		require.Len(t, prom.calls, 1, "exactly one evaluated window before promotion")
		assert.Equal(t, 100, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})
}

func reconcileCC(t *testing.T, r *CanaryConfigReconciler) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(t.Context(), ctrl.Request{Namespace: "default", Name: "cc"})
}

func TestReconcile(t *testing.T) {
	t.Run("deleted config is a no-op", func(t *testing.T) {
		_, r, _ := newTestEnv(&fakeFailureClient{}) // nothing seeded
		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})

	t.Run("terminal status is not progressed", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 100, "old": 0}, 30)
		cc.Status.Status = fv1.CanaryConfigStatusSucceeded
		_, r, c := newTestEnv(&fakeFailureClient{}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
		// weights untouched
		assert.Equal(t, 100, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})

	t.Run("invalid duration stops without requeue", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 30)
		cc.Spec.WeightIncrementDuration = "not-a-duration"
		_, r, _ := newTestEnv(&fakeFailureClient{}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})

	t.Run("non-positive duration stops without requeue", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 30)
		cc.Spec.WeightIncrementDuration = "0s"
		_, r, _ := newTestEnv(&fakeFailureClient{}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})

	t.Run("non-positive increment stops without requeue", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 0)
		_, r, c := newTestEnv(&fakeFailureClient{}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
		// weights untouched — no forever-requeue, no traffic shift
		assert.Equal(t, 0, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})

	t.Run("empty status is treated as pending and progressed", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 30)
		cc.Status.Status = "" // fresh create, status dropped by /status subresource
		_, r, c := newTestEnv(&fakeFailureClient{}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		// first step at weight 0 skips the failure check and increments
		assert.Equal(t, 30, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
		// status initialized to pending with the Progressing condition asserted
		got := getConfig(t, c)
		assert.Equal(t, fv1.CanaryConfigStatusPending, got.Status.Status)
		prog := apimeta.FindStatusCondition(got.Status.Conditions, fv1.CanaryConfigConditionProgressing)
		require.NotNil(t, prog)
		assert.Equal(t, metav1.ConditionTrue, prog.Status)
	})

	t.Run("under threshold increments and requeues after the interval", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		_, r, c := newTestEnv(&fakeFailureClient{pct: 5}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)
		assert.Equal(t, 60, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, fv1.CanaryConfigStatusPending, getConfig(t, c).Status.Status)
	})

	t.Run("reaching 100 writes Succeeded and stops", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 80, "old": 20}, 30)
		_, r, c := newTestEnv(&fakeFailureClient{pct: 5}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
		assert.Equal(t, fv1.CanaryConfigStatusSucceeded, getConfig(t, c).Status.Status)
	})

	t.Run("threshold crossed writes Failed and rolls back", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		_, r, c := newTestEnv(&fakeFailureClient{pct: 50}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, getConfig(t, c).Status.Status)
		assert.Equal(t, 0, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})

	t.Run("pair mode passes an empty function_version (regression)", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		prom := &fakeFailureClient{pct: 5}
		mgr, _, _ := newTestEnv(prom, trigger, cc)

		_, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)

		require.Len(t, prom.calls, 1)
		assert.Equal(t, "new", prom.calls[0].funcName)
		assert.Empty(t, prom.calls[0].funcVersion)
		assert.Equal(t, "default", prom.calls[0].funcNs)
	})
}

// aliasCanaryFixtures builds the alias-mode topology: two FunctionVersions of
// function "orders", a name-pinned FunctionAlias "prod" already pointing at
// the OLD version with no Weight/SecondaryVersion (the RFC's precondition:
// OLD already IS the primary before an alias-mode rollout starts), an
// HTTPTrigger referencing the alias, and a CanaryConfig pairing them.
// CanaryConfigSpec.NewFunction/OldFunction are FunctionVersion NAMES here,
// not function names — the RFC-0025 (secondary, primary) version role
// mapping (docs/rfc/0025-function-versions-aliases-rollback.md L182).
func aliasCanaryFixtures(increment, failureThreshold int) (trigger *fv1.HTTPTrigger, cc *fv1.CanaryConfig, alias *fv1.FunctionAlias, oldVer, newVer *fv1.FunctionVersion) {
	oldVer = &fv1.FunctionVersion{
		Name: "orders-v1", Namespace: "default",
		Spec: fv1.FunctionVersionSpec{FunctionName: "orders", Sequence: 1},
	}
	newVer = &fv1.FunctionVersion{
		Name: "orders-v2", Namespace: "default",
		Spec: fv1.FunctionVersionSpec{FunctionName: "orders", Sequence: 2},
	}
	alias = &fv1.FunctionAlias{
		Name: "prod", Namespace: "default", Generation: 1,
		Spec: fv1.FunctionAliasSpec{FunctionName: "orders", Version: "orders-v1"},
	}
	trigger = &fv1.HTTPTrigger{Name: "trig", Namespace: "default"}
	trigger.Spec.FunctionReference.Type = fv1.FunctionReferenceTypeFunctionName
	trigger.Spec.FunctionReference.Alias = "prod"
	cc = &fv1.CanaryConfig{
		Name: "cc", Namespace: "default", Generation: 1,
		Spec: fv1.CanaryConfigSpec{
			Trigger: "trig", NewFunction: "orders-v2", OldFunction: "orders-v1",
			WeightIncrement:         increment,
			WeightIncrementDuration: "1m",
			FailureThreshold:        failureThreshold,
		},
		Status: fv1.CanaryConfigStatus{Status: fv1.CanaryConfigStatusPending},
	}
	return trigger, cc, alias, oldVer, newVer
}

func getAliasByName(t *testing.T, c client.Client, name string) *fv1.FunctionAlias {
	t.Helper()
	got := &fv1.FunctionAlias{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: name}, got))
	return got
}

func TestStepAlias(t *testing.T) {
	t.Run("first step shifts weight down without touching version", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, "orders-v1", got.Spec.Version, "primary stays OLD for the whole progression")
		require.NotNil(t, got.Spec.Weight)
		assert.Equal(t, 70, *got.Spec.Weight)
		assert.Equal(t, "orders-v2", got.Spec.SecondaryVersion)
	})

	t.Run("primary weight 100 skips the failure check", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		prom := &fakeFailureClient{}
		mgr, _, _ := newTestEnv(prom, trigger, cc, alias, oldVer, newVer)

		_, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Empty(t, prom.calls, "at weight 100 the secondary has no traffic to evaluate")
	})

	t.Run("query passes the alias's function name, not the version name", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		weight := 70
		alias.Spec.Weight = &weight
		alias.Spec.SecondaryVersion = "orders-v2"
		prom := &fakeFailureClient{pct: 5}
		mgr, _, _ := newTestEnv(prom, trigger, cc, alias, oldVer, newVer)

		_, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)

		require.Len(t, prom.calls, 1)
		assert.Equal(t, "orders", prom.calls[0].funcName, "must be alias.Spec.FunctionName, not cfg.Spec.NewFunction")
		assert.Equal(t, "orders-v2", prom.calls[0].funcVersion)
		assert.Equal(t, "default", prom.calls[0].funcNs)
	})

	t.Run("weight reaching zero promotes with a single terminal write", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		weight := 10
		alias.Spec.Weight = &weight
		alias.Spec.SecondaryVersion = "orders-v2"
		mgr, _, c := newTestEnv(&fakeFailureClient{pct: 5}, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusSucceeded, out.terminalStatus)

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, "orders-v2", got.Spec.Version, "promotion repoints the primary to the new version")
		assert.Nil(t, got.Spec.Weight)
		assert.Equal(t, "", got.Spec.SecondaryVersion)
	})

	t.Run("increment >= 100 clamps the first step to a weighted non-terminal state", func(t *testing.T) {
		// Regression for the single-tick promote bug: WeightIncrement >= 100 used
		// to make the very first tick write the terminal promotion ({Version:
		// new, Weight: nil}) with the failure evaluation skipped entirely
		// (primaryWeight was still 100). The step from the unevaluated state must
		// clamp to {Weight: 0, SecondaryVersion: set}: all traffic on the
		// secondary, but Spec.Version still the OLD primary so the next tick's
		// evaluation can still roll back.
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(100, 10)
		prom := &fakeFailureClient{}
		mgr, _, c := newTestEnv(prom, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out, "introducing tick must not be terminal")
		assert.Empty(t, prom.calls, "at primary weight 100 there is nothing to evaluate")

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, "orders-v1", got.Spec.Version, "primary must stay OLD — no unevaluated promotion")
		require.NotNil(t, got.Spec.Weight)
		assert.Equal(t, 0, *got.Spec.Weight, "clamped to a weighted 0/100 split, not the terminal write")
		assert.Equal(t, "orders-v2", got.Spec.SecondaryVersion)
	})

	t.Run("increment >= 100 with failing secondary rolls back, never promotes", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(100, 10)
		prom := &fakeFailureClient{pct: 50} // > threshold 10 — secondary is failing
		mgr, _, c := newTestEnv(prom, trigger, cc, alias, oldVer, newVer)

		// Tick 1: clamped weighted step, evaluation skipped (no traffic yet).
		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)

		// Tick 2: first completed evaluation window — threshold crossed, roll back.
		out, err = mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		require.Len(t, prom.calls, 1)

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, "orders-v1", got.Spec.Version, "failing secondary must never be promoted")
		assert.Nil(t, got.Spec.Weight)
		assert.Equal(t, "", got.Spec.SecondaryVersion)
	})

	t.Run("increment >= 100 with healthy secondary promotes only after an evaluated window", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(100, 10)
		prom := &fakeFailureClient{pct: 5} // < threshold 10 — secondary is healthy
		mgr, _, c := newTestEnv(prom, trigger, cc, alias, oldVer, newVer)

		// Tick 1: the intermediate weighted write, not the promotion.
		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
		mid := getAliasByName(t, c, "prod")
		assert.Equal(t, "orders-v1", mid.Spec.Version)
		require.NotNil(t, mid.Spec.Weight)
		assert.Equal(t, 0, *mid.Spec.Weight)
		assert.Equal(t, "orders-v2", mid.Spec.SecondaryVersion)

		// Tick 2: evaluation ran and passed — terminal promotion.
		out, err = mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusSucceeded, out.terminalStatus)
		require.Len(t, prom.calls, 1, "exactly one evaluated window before promotion")

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, "orders-v2", got.Spec.Version)
		assert.Nil(t, got.Spec.Weight)
		assert.Equal(t, "", got.Spec.SecondaryVersion)
	})

	t.Run("increment 50 happy path keeps its two-tick shape", func(t *testing.T) {
		// Matches the integration suite's IncrementStep=50 rollout: 100 -> 50
		// (first weighted step), evaluate at 50, then 50 -> terminal in the SAME
		// tick as the passing evaluation — the invariant gate must not add an
		// interval when the terminal step starts from an already-evaluated state.
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(50, 10)
		prom := &fakeFailureClient{pct: 5}
		mgr, _, c := newTestEnv(prom, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
		mid := getAliasByName(t, c, "prod")
		require.NotNil(t, mid.Spec.Weight)
		assert.Equal(t, 50, *mid.Spec.Weight)

		out, err = mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusSucceeded, out.terminalStatus, "50 -> terminal must complete in the evaluation-passing tick")
		require.Len(t, prom.calls, 1)
		assert.Equal(t, "orders-v2", getAliasByName(t, c, "prod").Spec.Version)
	})

	t.Run("failure threshold crossed rolls back without changing version", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		weight := 70
		alias.Spec.Weight = &weight
		alias.Spec.SecondaryVersion = "orders-v2"
		mgr, _, c := newTestEnv(&fakeFailureClient{pct: 50}, trigger, cc, alias, oldVer, newVer) // > threshold 10

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		assert.Empty(t, out.message, "threshold-crossed keeps the default Failed condition message")

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, "orders-v1", got.Spec.Version, "primary must stay OLD — zero History appends on rollback")
		assert.Nil(t, got.Spec.Weight)
		assert.Equal(t, "", got.Spec.SecondaryVersion)
	})

	t.Run("missing alias fails without writing anything", func(t *testing.T) {
		trigger, cc, _, oldVer, newVer := aliasCanaryFixtures(30, 10)
		mgr, _, _ := newTestEnv(&fakeFailureClient{}, trigger, cc, oldVer, newVer) // alias not seeded

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		assert.Contains(t, out.message, "not found")
	})

	t.Run("digest-pinned alias is refused and never written", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		alias.Spec.Version = ""
		alias.Spec.PackageDigest = "sha256:" + strings.Repeat("a", 64)
		wantSpec := alias.Spec
		mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		assert.Contains(t, out.message, "digest-pinned")

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, wantSpec, got.Spec, "a refused validation must never write the alias")
	})

	t.Run("spec-managed alias is refused and never written", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		alias.Annotations = map[string]string{fv1.SpecDeploymentUIDAnnotation: "some-deploy-uid"}
		wantSpec := alias.Spec
		mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		assert.Contains(t, out.message, "fission spec")

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, wantSpec, got.Spec, "a refused validation must never write the alias")
	})

	t.Run("spec-managed alias with a present-but-empty annotation value is still refused", func(t *testing.T) {
		// Key PRESENCE marks an alias spec-managed, matching
		// pkg/fission-cli/cmd/function/rollback.go's guard — an empty value is
		// still a `fission spec` stamp.
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		alias.Annotations = map[string]string{fv1.SpecDeploymentUIDAnnotation: ""}
		wantSpec := alias.Spec
		mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		assert.Contains(t, out.message, "fission spec")

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, wantSpec, got.Spec, "a refused validation must never write the alias")
	})

	t.Run("alias not currently pointing at OldFunction is refused and never written", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		alias.Spec.Version = "orders-v0" // some third version, not cfg.Spec.OldFunction
		wantSpec := alias.Spec
		mgr, _, c := newTestEnv(&fakeFailureClient{}, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		assert.Contains(t, out.message, "orders-v0")
		assert.Contains(t, out.message, "orders-v1")

		got := getAliasByName(t, c, "prod")
		assert.Equal(t, wantSpec, got.Spec, "a refused validation must never write the alias — the first progression write must not silently repoint it")
	})

	t.Run("new-function version belonging to a different function is refused", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		newVer.Spec.FunctionName = "other-fn"
		mgr, _, _ := newTestEnv(&fakeFailureClient{}, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		assert.Contains(t, out.message, "orders-v2")
	})

	t.Run("old-function version belonging to a different function is refused", func(t *testing.T) {
		trigger, cc, alias, oldVer, newVer := aliasCanaryFixtures(30, 10)
		oldVer.Spec.FunctionName = "other-fn"
		mgr, _, _ := newTestEnv(&fakeFailureClient{}, trigger, cc, alias, oldVer, newVer)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)
		assert.Contains(t, out.message, "orders-v1")
	})
}
