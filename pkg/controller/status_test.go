// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

// Compile-time proof the generated CRD types satisfy ConditionedObject via the
// hand-written GetConditions accessors.
var (
	_ ConditionedObject = (*fv1.TimeTrigger)(nil)
	_ ConditionedObject = (*fv1.Package)(nil)
)

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithStatusSubresource(&fv1.TimeTrigger{}).
		Build()
}

func TestSetConditions_WritesAndStampsGeneration(t *testing.T) {
	tt := &fv1.TimeTrigger{ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "ns", Generation: 3}}
	c := newFakeClient(t, tt)
	ctx := t.Context()

	SetConditions(ctx, logr.Discard(), c, tt, metav1.Condition{
		Type: fv1.TimeTriggerConditionReady, Status: metav1.ConditionTrue, Reason: fv1.TimeTriggerReasonCronRegistered,
	})

	got := &fv1.TimeTrigger{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tt), got))
	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionTrue, got.Status.Conditions[0].Status)
	// ObservedGeneration is stamped from the object, not pre-filled by the caller.
	assert.Equal(t, int64(3), got.Status.Conditions[0].ObservedGeneration)
}

func TestSetConditions_FastPathSkipsWrite(t *testing.T) {
	tt := &fv1.TimeTrigger{ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "ns", Generation: 1}}
	c := newFakeClient(t, tt)
	ctx := t.Context()
	want := metav1.Condition{Type: fv1.TimeTriggerConditionReady, Status: metav1.ConditionTrue, Reason: fv1.TimeTriggerReasonCronRegistered}

	SetConditions(ctx, logr.Discard(), c, tt, want)
	first := &fv1.TimeTrigger{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tt), first))
	rv := first.ResourceVersion

	// Re-asserting the same condition must not issue a second status Update
	// (ResourceVersion would change if it did).
	SetConditions(ctx, logr.Discard(), c, first, want)
	second := &fv1.TimeTrigger{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tt), second))
	assert.Equal(t, rv, second.ResourceVersion, "no status write when nothing changes")
}

func TestSetConditions_NoWantIsNoop(t *testing.T) {
	tt := &fv1.TimeTrigger{ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "ns"}}
	c := newFakeClient(t, tt)
	ctx := t.Context()
	SetConditions(ctx, logr.Discard(), c, tt) // no want
	got := &fv1.TimeTrigger{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tt), got))
	assert.Empty(t, got.Status.Conditions)
}
