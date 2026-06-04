// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package funcreconciler

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

func newReconcilerWith(c client.Client, finalizer bool, nd *fakeBackend) *functionReconciler {
	return &functionReconciler{
		logger:    logr.Discard(),
		client:    c,
		finalizer: finalizer,
		backends:  map[fv1.ExecutorType]executortype.FuncReconciler{fv1.ExecutorTypeNewdeploy: nd},
	}
}

func TestFunctionReconcilerFinalizer(t *testing.T) {
	key := types.NamespacedName{Name: "fn", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	t.Run("enabled: a live function gets the finalizer added and is still reconciled", func(t *testing.T) {
		nd := &fakeBackend{}
		c := crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))
		r := newReconcilerWith(c, true, nd)

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		got := &fv1.Function{}
		require.NoError(t, c.Get(t.Context(), key, got))
		assert.True(t, controllerutil.ContainsFinalizer(got, functionFinalizer), "finalizer must be added")
		assert.Len(t, nd.reconciled, 1, "reconcile must still run after adding the finalizer")
	})

	t.Run("disabled: an existing finalizer is drained and the function is still reconciled", func(t *testing.T) {
		nd := &fakeBackend{}
		f := fn("fn", fv1.ExecutorTypeNewdeploy, "u1")
		f.Finalizers = []string{functionFinalizer}
		c := crClient(f)
		r := newReconcilerWith(c, false, nd)

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		got := &fv1.Function{}
		require.NoError(t, c.Get(t.Context(), key, got))
		assert.False(t, controllerutil.ContainsFinalizer(got, functionFinalizer), "finalizer must be drained when disabled")
		assert.Len(t, nd.reconciled, 1)
	})

	t.Run("deletion with our finalizer tears the workloads down then releases the finalizer", func(t *testing.T) {
		nd := &fakeBackend{}
		f := fn("fn", fv1.ExecutorTypeNewdeploy, "u1")
		f.Finalizers = []string{functionFinalizer}
		c := crClient(f)
		// Delete sets DeletionTimestamp (the finalizer keeps the object around).
		require.NoError(t, c.Delete(t.Context(), f))
		r := newReconcilerWith(c, true, nd)
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		assert.Equal(t, []string{"fn"}, nd.deleted, "workloads must be torn down on delete")
		got := &fv1.Function{}
		err = c.Get(t.Context(), key, got)
		assert.True(t, apierrors.IsNotFound(err), "removing the last finalizer lets the object be collected")
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached, "cache entry dropped after teardown")
	})

	t.Run("deletion teardown error keeps the finalizer for a retry", func(t *testing.T) {
		nd := &fakeBackend{deleteErr: assert.AnError}
		f := fn("fn", fv1.ExecutorTypeNewdeploy, "u1")
		f.Finalizers = []string{functionFinalizer}
		c := crClient(f)
		require.NoError(t, c.Delete(t.Context(), f))
		r := newReconcilerWith(c, true, nd)

		_, err := r.Reconcile(t.Context(), req)
		require.Error(t, err)

		got := &fv1.Function{}
		require.NoError(t, c.Get(t.Context(), key, got))
		assert.True(t, controllerutil.ContainsFinalizer(got, functionFinalizer), "finalizer kept so teardown is retried")
	})

	t.Run("deletion without our finalizer is a no-op and leaves the cache for the NotFound path", func(t *testing.T) {
		nd := &fakeBackend{}
		f := fn("fn", fv1.ExecutorTypeNewdeploy, "u1")
		f.Finalizers = []string{"someone-else/keep"}
		c := crClient(f)
		require.NoError(t, c.Delete(t.Context(), f))
		r := newReconcilerWith(c, true, nd)
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		assert.Empty(t, nd.deleted, "not our finalizer: teardown deferred to the NotFound path")
		_, cached := r.lastReconciled.Load(key)
		assert.True(t, cached, "cache retained for the NotFound path to tear down later")
	})
}

// clientWithUpdateErr builds a fake client seeded with objs whose Update calls
// all fail with updateErr — exercising the finalizer Update race tolerance.
func clientWithUpdateErr(updateErr error, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				return updateErr
			},
		}).
		Build()
}

func TestFunctionReconcilerFinalizerUpdateRace(t *testing.T) {
	key := types.NamespacedName{Name: "fn", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}
	gr := fv1.SchemeGroupVersion.WithResource("functions").GroupResource()

	deletingFn := func() *fv1.Function {
		f := fn("fn", fv1.ExecutorTypeNewdeploy, "u1")
		// The fake client rejects a DeletionTimestamp object without a finalizer,
		// so seed one and Delete() it to set the timestamp.
		f.Finalizers = []string{functionFinalizer}
		return f
	}

	t.Run("delete path: finalizer-remove Update returns NotFound -> tolerated, no requeue", func(t *testing.T) {
		nd := &fakeBackend{}
		c := clientWithUpdateErr(apierrors.NewNotFound(gr, "fn"), deletingFn())
		require.NoError(t, c.Delete(t.Context(), deletingFn()))
		r := newReconcilerWith(c, true, nd)
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))

		res, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
		assert.Equal(t, []string{"fn"}, nd.deleted, "teardown must have run before the Update")
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached, "cache entry dropped on the NotFound branch")
	})

	t.Run("delete path: finalizer-remove Update returns Conflict -> requeue, no error", func(t *testing.T) {
		nd := &fakeBackend{}
		c := clientWithUpdateErr(apierrors.NewConflict(gr, "fn", assert.AnError), deletingFn())
		require.NoError(t, c.Delete(t.Context(), deletingFn()))
		r := newReconcilerWith(c, true, nd)

		res, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{Requeue: true}, res)
	})

	t.Run("add path: finalizer-add Update returns NotFound -> tolerated, no requeue", func(t *testing.T) {
		nd := &fakeBackend{}
		c := clientWithUpdateErr(apierrors.NewNotFound(gr, "fn"), fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))
		r := newReconcilerWith(c, true, nd)

		res, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})

	t.Run("add path: finalizer-add Update returns Conflict -> requeue, no error", func(t *testing.T) {
		nd := &fakeBackend{}
		c := clientWithUpdateErr(apierrors.NewConflict(gr, "fn", assert.AnError), fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))
		r := newReconcilerWith(c, true, nd)

		res, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{Requeue: true}, res)
	})
}

func TestDeletionTimestampPredicate(t *testing.T) {
	now := metav1.Now()
	live := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn"}}
	deleting := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", DeletionTimestamp: &now}}

	assert.True(t, deletionTimestampPredicate.Update(event.UpdateEvent{ObjectNew: deleting}),
		"must pass updates where the object is being deleted (Generation unchanged, so GenerationChangedPredicate drops them)")
	assert.False(t, deletionTimestampPredicate.Update(event.UpdateEvent{ObjectNew: live}),
		"must not pass a normal update on its own — GenerationChangedPredicate handles those")
}
