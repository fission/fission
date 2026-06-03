// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package funcreconciler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestOwnedObjectToFunction(t *testing.T) {
	t.Run("maps a managed object back to its owning Function by label", func(t *testing.T) {
		obj := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name:      "newdeploy-fn-default-abc", // workload name, not the function name
			Namespace: "fission-function",         // workload namespace, not the function namespace
			Labels: map[string]string{
				fv1.FUNCTION_NAME:      "fn",
				fv1.FUNCTION_NAMESPACE: "default",
			},
		}}
		reqs := ownedObjectToFunction(t.Context(), obj)
		assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "fn", Namespace: "default"}}}, reqs,
			"must point at the Function CR (from labels), not the workload name/namespace")
	})

	t.Run("returns nil for an object without the function labels", func(t *testing.T) {
		obj := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "y"}}
		assert.Nil(t, ownedObjectToFunction(t.Context(), obj))
	})
}

func TestDeleteOnlyPredicate(t *testing.T) {
	assert.True(t, deleteOnlyPredicate.Delete(event.DeleteEvent{}),
		"a deleted backing object must re-enqueue the owning Function")
	assert.False(t, deleteOnlyPredicate.Create(event.CreateEvent{}),
		"the executor creates these objects itself — no need to react")
	assert.False(t, deleteOnlyPredicate.Update(event.UpdateEvent{}),
		"must NOT react to updates — the idle reaper scales the Deployment and we'd fight it")
	assert.False(t, deleteOnlyPredicate.Generic(event.GenericEvent{}))
}
