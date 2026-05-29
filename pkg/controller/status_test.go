// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fissioncorev1 "github.com/fission/fission/pkg/generated/clientset/versioned/typed/core/v1"
)

// Compile-time proof that the generated namespaced typed clients satisfy the
// generic StatusClient interface — this is what lets call sites pass
// fissionClient.CoreV1().TimeTriggers(ns) straight into SetConditions.
var (
	_ StatusClient[*fv1.TimeTrigger] = fissioncorev1.TimeTriggerInterface(nil)
	_ StatusClient[*fv1.Package]     = fissioncorev1.PackageInterface(nil)
	_ ConditionedObject              = (*fv1.TimeTrigger)(nil)
)

type fakeStatusClient struct {
	obj         *fv1.TimeTrigger
	getErr      error
	updateErr   error
	updateCalls int
}

func (f *fakeStatusClient) Get(_ context.Context, _ string, _ metav1.GetOptions) (*fv1.TimeTrigger, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.obj.DeepCopy(), nil
}

func (f *fakeStatusClient) UpdateStatus(_ context.Context, obj *fv1.TimeTrigger, _ metav1.UpdateOptions) (*fv1.TimeTrigger, error) {
	f.updateCalls++
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.obj = obj.DeepCopy()
	return f.obj, nil
}

func newTrigger(gen int64, conds ...metav1.Condition) *fv1.TimeTrigger {
	return &fv1.TimeTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "ns", Generation: gen},
		Status:     fv1.TimeTriggerStatus{Conditions: conds},
	}
}

func TestSetConditions_WritesAndStampsGeneration(t *testing.T) {
	server := newTrigger(3)
	fc := &fakeStatusClient{obj: server}
	in := newTrigger(3)

	SetConditions(context.Background(), logr.Discard(), fc, in, metav1.Condition{
		Type: fv1.TimeTriggerConditionReady, Status: metav1.ConditionTrue, Reason: fv1.TimeTriggerReasonCronRegistered,
	})

	require.Equal(t, 1, fc.updateCalls)
	got := fc.obj.Status.Conditions
	require.Len(t, got, 1)
	assert.Equal(t, fv1.TimeTriggerConditionReady, got[0].Type)
	assert.Equal(t, metav1.ConditionTrue, got[0].Status)
	// ObservedGeneration is stamped from the object, not pre-filled by the caller.
	assert.Equal(t, int64(3), got[0].ObservedGeneration)
}

func TestSetConditions_FastPathSkipsWrite(t *testing.T) {
	want := metav1.Condition{
		Type: fv1.TimeTriggerConditionReady, Status: metav1.ConditionTrue,
		Reason: fv1.TimeTriggerReasonCronRegistered, ObservedGeneration: 5,
	}
	// In-hand object already carries the desired condition at this generation.
	in := newTrigger(5, want)
	// Get would error if reached — proves the fast-path skipped it.
	fc := &fakeStatusClient{obj: in, getErr: errors.New("Get must not be called on the fast-path")}

	SetConditions(context.Background(), logr.Discard(), fc, in, metav1.Condition{
		Type: fv1.TimeTriggerConditionReady, Status: metav1.ConditionTrue, Reason: fv1.TimeTriggerReasonCronRegistered,
	})

	assert.Equal(t, 0, fc.updateCalls, "no UpdateStatus when nothing changes")
}

func TestSetConditions_NoWantIsNoop(t *testing.T) {
	fc := &fakeStatusClient{obj: newTrigger(1), getErr: errors.New("must not be called")}
	SetConditions(context.Background(), logr.Discard(), fc, newTrigger(1))
	assert.Equal(t, 0, fc.updateCalls)
}
