// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
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
			setCanaryConfigConditions(&s, tt.status, 7)

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

func TestCanaryConfigCancelFuncMap(t *testing.T) {
	t.Parallel()
	m := makecanaryConfigCancelFuncMap()
	meta := &metav1.ObjectMeta{Name: "cc", Namespace: "default"}

	_, err := m.lookup(meta)
	require.Error(t, err, "lookup of an absent key errors")

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	info := &CanaryProcessingInfo{Ticker: ticker}
	require.NoError(t, m.assign(meta, info))

	got, err := m.lookup(meta)
	require.NoError(t, err)
	assert.Same(t, info, got)

	m.remove(meta)
	_, err = m.lookup(meta)
	require.Error(t, err, "lookup after remove errors")
}

func TestKeyFromMetadata(t *testing.T) {
	t.Parallel()
	k := keyFromMetadata(&metav1.ObjectMeta{Name: "n", Namespace: "ns"})
	assert.Equal(t, metadataKey{Name: "n", Namespace: "ns"}, k)
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

func canaryFixtures(weights map[string]int, increment int) (*fv1.HTTPTrigger, *fv1.CanaryConfig) {
	trigger := &fv1.HTTPTrigger{ObjectMeta: metav1.ObjectMeta{Name: "trig", Namespace: "default"}}
	trigger.Spec.FunctionReference.FunctionWeights = weights
	cc := &fv1.CanaryConfig{ObjectMeta: metav1.ObjectMeta{Name: "cc", Namespace: "default"}}
	cc.Spec = fv1.CanaryConfigSpec{Trigger: "trig", NewFunction: "new", OldFunction: "old", WeightIncrement: increment}
	return trigger, cc
}

func TestRollForward(t *testing.T) {
	t.Run("increments weights without completing", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 0, "old": 100}, 30)
		mgr := &canaryConfigMgr{logger: logr.Discard(), fissionClient: fClient.NewSimpleClientset(trigger, cc)} //nolint:staticcheck // NewClientset Update hits kubernetes/kubernetes#126850 for our CRDs

		done, err := mgr.rollForward(t.Context(), cc, trigger)
		require.NoError(t, err)
		assert.False(t, done)
		assert.Equal(t, 30, trigger.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 70, trigger.Spec.FunctionReference.FunctionWeights["old"])
	})

	t.Run("completes when the increment reaches 100", func(t *testing.T) {
		trigger, cc := canaryFixtures(map[string]int{"new": 80, "old": 20}, 30)
		mgr := &canaryConfigMgr{logger: logr.Discard(), fissionClient: fClient.NewSimpleClientset(trigger, cc)} //nolint:staticcheck // NewClientset Update hits kubernetes/kubernetes#126850 for our CRDs

		done, err := mgr.rollForward(t.Context(), cc, trigger)
		require.NoError(t, err)
		assert.True(t, done)
		assert.Equal(t, 100, trigger.Spec.FunctionReference.FunctionWeights["new"])
		assert.Equal(t, 0, trigger.Spec.FunctionReference.FunctionWeights["old"])
	})
}

func TestRollback(t *testing.T) {
	trigger, cc := canaryFixtures(map[string]int{"new": 50, "old": 50}, 30)
	mgr := &canaryConfigMgr{logger: logr.Discard(), fissionClient: fClient.NewSimpleClientset(trigger, cc)} //nolint:staticcheck // NewClientset Update hits kubernetes/kubernetes#126850 for our CRDs

	require.NoError(t, mgr.rollback(t.Context(), cc, trigger))
	assert.Equal(t, 0, trigger.Spec.FunctionReference.FunctionWeights["new"])
	assert.Equal(t, 100, trigger.Spec.FunctionReference.FunctionWeights["old"])

	// canary config status moved to failed
	updated, err := mgr.fissionClient.CoreV1().CanaryConfigs("default").Get(t.Context(), "cc", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, fv1.CanaryConfigStatusFailed, updated.Status.Status)
}
