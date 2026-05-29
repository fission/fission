// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
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
			require.True(t, setCanaryConfigConditions(&s, tt.status, 7), "first set reports a change")
			assert.False(t, setCanaryConfigConditions(&s, tt.status, 7), "re-setting the same conditions reports no change")

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
	got := c.getFunctionQueryLabels("fn", "ns", "/p", "GET")
	assert.Equal(t, `function_name="fn",function_namespace="ns",path="/p",method="GET"`, got)
}

func TestMakePrometheusClient(t *testing.T) {
	t.Parallel()
	c, err := MakePrometheusClient(logr.Discard(), "http://prometheus.monitoring:9090")
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// fakeFailureClient is a deterministic failurePercentageGetter for tests.
type fakeFailureClient struct {
	pct float64
	err error
}

func (f fakeFailureClient) GetFunctionFailurePercentage(_ context.Context, _ string, _ []string, _, _, _ string) (float64, error) {
	return f.pct, f.err
}

func canaryFixtures(weights map[string]int, increment int) (*fv1.HTTPTrigger, *fv1.CanaryConfig) {
	trigger := &fv1.HTTPTrigger{ObjectMeta: metav1.ObjectMeta{Name: "trig", Namespace: "default"}}
	trigger.Spec.FunctionReference.Type = fv1.FunctionReferenceTypeFunctionWeights
	trigger.Spec.FunctionReference.FunctionWeights = weights
	cc := &fv1.CanaryConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cc", Namespace: "default", Generation: 1},
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
	mgr := &canaryConfigMgr{logger: logr.Discard(), client: c, promClient: prom}
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
		mgr, _, c := newTestEnv(fakeFailureClient{}, trigger, cc)

		done, err := mgr.rollForward(t.Context(), cc, trigger)
		require.NoError(t, err)
		assert.False(t, done)

		got := getTrigger(t, c)
		assert.Equal(t, 30, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 70, got.Spec.FunctionReference.FunctionWeights["old"])
	})

	t.Run("completes when the increment reaches 100", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 80, "old": 20}, 30)
		mgr, _, c := newTestEnv(fakeFailureClient{}, trigger, cc)

		done, err := mgr.rollForward(t.Context(), cc, trigger)
		require.NoError(t, err)
		assert.True(t, done)

		got := getTrigger(t, c)
		assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["old"])
	})
}

func TestRollbackWeights(t *testing.T) {
	trigger, cc := canaryFixtures(map[string]int{"new": 50, "old": 50}, 30)
	mgr, _, c := newTestEnv(fakeFailureClient{}, trigger, cc)

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
		mgr, _, _ := newTestEnv(fakeFailureClient{}, cc) // no trigger seeded

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
	})

	t.Run("no traffic requeues without changing weights", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		mgr, _, c := newTestEnv(fakeFailureClient{pct: -1}, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
		assert.Equal(t, 30, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})

	t.Run("failure query error requeues", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		mgr, _, _ := newTestEnv(fakeFailureClient{err: assert.AnError}, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
	})

	t.Run("threshold crossed rolls back and reports Failed", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		mgr, _, c := newTestEnv(fakeFailureClient{pct: 50}, trigger, cc) // > threshold 10

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, out.terminalStatus)

		got := getTrigger(t, c)
		assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["old"])
	})

	t.Run("under threshold increments and requeues", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		mgr, _, c := newTestEnv(fakeFailureClient{pct: 5}, trigger, cc) // < threshold 10

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, stepOutcome{requeue: true}, out)
		assert.Equal(t, 60, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})

	t.Run("reaching 100 reports Succeeded", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 80, "old": 20}, 30)
		mgr, _, c := newTestEnv(fakeFailureClient{pct: 5}, trigger, cc)

		out, err := mgr.step(t.Context(), cc)
		require.NoError(t, err)
		assert.Equal(t, fv1.CanaryConfigStatusSucceeded, out.terminalStatus)

		got := getTrigger(t, c)
		assert.Equal(t, 100, got.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 0, got.Spec.FunctionReference.FunctionWeights["old"])
	})
}

func reconcileCC(t *testing.T, r *CanaryConfigReconciler) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "cc"}})
}

func TestReconcile(t *testing.T) {
	t.Run("deleted config is a no-op", func(t *testing.T) {
		_, r, _ := newTestEnv(fakeFailureClient{}) // nothing seeded
		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})

	t.Run("terminal status is not progressed", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 100, "old": 0}, 30)
		cc.Status.Status = fv1.CanaryConfigStatusSucceeded
		_, r, c := newTestEnv(fakeFailureClient{}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
		// weights untouched
		assert.Equal(t, 100, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})

	t.Run("invalid duration stops without requeue", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 30)
		cc.Spec.WeightIncrementDuration = "not-a-duration"
		_, r, _ := newTestEnv(fakeFailureClient{}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})

	t.Run("empty status is treated as pending and progressed", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 30)
		cc.Status.Status = "" // fresh create, status dropped by /status subresource
		_, r, c := newTestEnv(fakeFailureClient{}, trigger, cc)

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
		_, r, c := newTestEnv(fakeFailureClient{pct: 5}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)
		assert.Equal(t, 60, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, fv1.CanaryConfigStatusPending, getConfig(t, c).Status.Status)
	})

	t.Run("reaching 100 writes Succeeded and stops", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 80, "old": 20}, 30)
		_, r, c := newTestEnv(fakeFailureClient{pct: 5}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
		assert.Equal(t, fv1.CanaryConfigStatusSucceeded, getConfig(t, c).Status.Status)
	})

	t.Run("threshold crossed writes Failed and rolls back", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 30, "old": 70}, 30)
		_, r, c := newTestEnv(fakeFailureClient{pct: 50}, trigger, cc)

		res, err := reconcileCC(t, r)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
		assert.Equal(t, fv1.CanaryConfigStatusFailed, getConfig(t, c).Status.Status)
		assert.Equal(t, 0, getTrigger(t, c).Spec.FunctionReference.FunctionWeights["new"])
	})
}
