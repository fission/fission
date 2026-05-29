// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

func TestTimeTriggerReconciler(t *testing.T) {
	tt := &fv1.TimeTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "cron1", Namespace: "default", Generation: 1},
		Spec:       fv1.TimeTriggerSpec{Cron: "0 0 * * *", FunctionReference: fv1.FunctionReference{Name: "fn"}},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(tt).
		WithStatusSubresource(&fv1.TimeTrigger{}).
		Build()
	r := &TimeTriggerReconciler{
		logger: logr.Discard(),
		client: c,
		timer:  MakeTimer(logr.Discard(), "http://router.fission"),
	}
	key := types.NamespacedName{Namespace: "default", Name: "cron1"}
	req := ctrl.Request{NamespacedName: key}
	ctx := t.Context()

	// Add/Update: cron registered and conditions written.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, ok := r.timer.triggers[key]
	assert.True(t, ok, "cron entry should be registered after reconcile")

	got := &fv1.TimeTrigger{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tt), got))
	assert.True(t, conditions.IsTrue(got.Status.Conditions, fv1.TimeTriggerConditionScheduled), "Scheduled condition should be True")
	assert.True(t, conditions.IsTrue(got.Status.Conditions, fv1.TimeTriggerConditionReady), "Ready condition should be True")

	// Reconcile again is idempotent (no error, entry still present).
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Len(t, r.timer.triggers, 1)

	// Delete: NotFound from the client tears the cron entry down.
	require.NoError(t, c.Delete(ctx, got))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, ok = r.timer.triggers[key]
	assert.False(t, ok, "cron entry should be removed after the trigger is deleted")
}
