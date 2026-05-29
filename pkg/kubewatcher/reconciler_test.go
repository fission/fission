// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatcher

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/publisher"
)

func TestKubernetesWatchTriggerReconciler(t *testing.T) {
	w := &fv1.KubernetesWatchTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "kwt1", Namespace: "default", Generation: 1},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Type:              "POD",
			FunctionReference: fv1.FunctionReference{Name: "fn"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(w).
		WithStatusSubresource(&fv1.KubernetesWatchTrigger{}).
		Build()
	kc := kubefake.NewClientset()
	kc.PrependWatchReactor("pods", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil
	})

	r := &KubernetesWatchTriggerReconciler{
		logger:      logr.Discard(),
		client:      c,
		kubeWatcher: MakeKubeWatcher(t.Context(), logr.Discard(), kc, publisher.MakeWebhookPublisher(logr.Discard(), "http://router.fission")),
	}
	key := types.NamespacedName{Namespace: "default", Name: "kwt1"}
	req := ctrl.Request{NamespacedName: key}
	ctx := t.Context()

	// Add: watch registered and conditions written.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, ok := r.kubeWatcher.watches[key]
	assert.True(t, ok, "watch subscription should be registered after reconcile")

	got := &fv1.KubernetesWatchTrigger{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(w), got))
	assert.True(t, conditions.IsTrue(got.Status.Conditions, fv1.KubernetesWatchTriggerConditionSubscribed))
	assert.True(t, conditions.IsTrue(got.Status.Conditions, fv1.KubernetesWatchTriggerConditionReady))

	// Re-reconcile is idempotent and must not leave a second subscription.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Len(t, r.kubeWatcher.watches, 1, "re-reconcile must replace, not leak, the watch")

	// Delete: NotFound tears the watch down.
	require.NoError(t, c.Delete(ctx, got))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, ok = r.kubeWatcher.watches[key]
	assert.False(t, ok, "watch should be removed after the trigger is deleted")
}

// TestKubernetesWatchTriggerReconciler_FailedRestartStopsStaleWatch covers the
// spec-change-to-invalid case: a running watch must be stopped before the
// replacement is attempted, so a failed (re)start leaves no stale watch firing
// for the superseded config.
func TestKubernetesWatchTriggerReconciler_FailedRestartStopsStaleWatch(t *testing.T) {
	// Fail fast instead of running the 60×500ms production retry loop.
	orig := watchStartRetries
	watchStartRetries = 1
	t.Cleanup(func() { watchStartRetries = orig })

	w := &fv1.KubernetesWatchTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "kwt1", Namespace: "default", Generation: 1},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Type:              "POD",
			FunctionReference: fv1.FunctionReference{Name: "fn"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(w).
		WithStatusSubresource(&fv1.KubernetesWatchTrigger{}).
		Build()
	kc := kubefake.NewClientset()
	kc.PrependWatchReactor("pods", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil
	})
	r := &KubernetesWatchTriggerReconciler{
		logger:      logr.Discard(),
		client:      c,
		kubeWatcher: MakeKubeWatcher(t.Context(), logr.Discard(), kc, publisher.MakeWebhookPublisher(logr.Discard(), "http://router.fission")),
	}
	key := types.NamespacedName{Namespace: "default", Name: "kwt1"}
	req := ctrl.Request{NamespacedName: key}
	ctx := t.Context()

	// First reconcile: a valid POD watch is registered.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, ok := r.kubeWatcher.watches[key]
	require.True(t, ok)

	// Mutate the spec to an unsupported type so the next watch start fails.
	cur := &fv1.KubernetesWatchTrigger{}
	require.NoError(t, c.Get(ctx, key, cur))
	cur.Spec.Type = "NOPE"
	require.NoError(t, c.Update(ctx, cur))

	_, err = r.Reconcile(ctx, req)
	require.Error(t, err, "an unstartable watch should surface an error for requeue")
	_, ok = r.kubeWatcher.watches[key]
	assert.False(t, ok, "the stale watch must be stopped/removed when the replacement can't start")

	got := &fv1.KubernetesWatchTrigger{}
	require.NoError(t, c.Get(ctx, key, got))
	assert.False(t, conditions.IsTrue(got.Status.Conditions, fv1.KubernetesWatchTriggerConditionReady), "trigger should be not-Ready after a failed watch start")
}
