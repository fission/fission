// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"context"
	"errors"
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
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/memory"
)

func stateFn(name, ns string, sc *fv1.StateConfig) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       fv1.FunctionSpec{State: sc},
	}
}

func newTestReconciler(t *testing.T, objs ...client.Object) (*functionStateReconciler, client.Client, statestore.KVStore) {
	t.Helper()
	inner, err := memory.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	kv, err := inner.KV()
	require.NoError(t, err)

	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
	r := &functionStateReconciler{
		logger: logr.Discard(),
		client: c,
		index:  NewFunctionIndex(),
		kv:     kv,
	}
	return r, c, kv
}

func reconcile(t *testing.T, r *functionStateReconciler, name, ns string) {
	t.Helper()
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}})
	require.NoError(t, err)
}

func seedKeys(t *testing.T, kv statestore.KVStore, ns, keyspace string, keys ...string) {
	t.Helper()
	sc := statestore.Scope{Namespace: ns, Owner: StateOwner, Keyspace: keyspace}
	for _, k := range keys {
		require.NoError(t, kv.Set(t.Context(), sc, k, []byte("v"), statestore.SetOptions{}))
	}
}

func keyCount(t *testing.T, kv statestore.KVStore, ns, keyspace string) int {
	t.Helper()
	sc := statestore.Scope{Namespace: ns, Owner: StateOwner, Keyspace: keyspace}
	kp, err := kv.List(t.Context(), sc, "", statestore.Page{})
	require.NoError(t, err)
	return len(kp.Keys)
}

func TestReconcilerAddsFinalizerAndIndexes(t *testing.T) {
	t.Parallel()
	fn := stateFn("f1", "ns", &fv1.StateConfig{})
	r, c, _ := newTestReconciler(t, fn)

	reconcile(t, r, "f1", "ns")

	assert.True(t, r.index.Known("ns", "f1"), "keyspace defaults to fn name")
	got := &fv1.Function{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "f1", Namespace: "ns"}, got))
	assert.Contains(t, got.Finalizers, stateFinalizer)
}

func TestReconcilerOptOutReleasesFinalizerAndIndex(t *testing.T) {
	t.Parallel()
	fn := stateFn("f1", "ns", &fv1.StateConfig{})
	fn.Finalizers = []string{stateFinalizer}
	fn.Spec.State = nil
	r, c, _ := newTestReconciler(t, fn)

	reconcile(t, r, "f1", "ns")

	assert.False(t, r.index.Known("ns", "f1"))
	got := &fv1.Function{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "f1", Namespace: "ns"}, got))
	assert.NotContains(t, got.Finalizers, stateFinalizer, "opt-out must never wedge a later delete")
}

func TestReconcilerPurgesKeyspaceOnDelete(t *testing.T) {
	t.Parallel()
	fn := stateFn("f1", "ns", &fv1.StateConfig{Keyspace: "carts"})
	fn.Finalizers = []string{stateFinalizer}
	r, c, kv := newTestReconciler(t, fn)
	reconcile(t, r, "f1", "ns") // index it
	seedKeys(t, kv, "ns", "carts", "a", "b", "c")

	require.NoError(t, c.Delete(t.Context(), fn))
	reconcile(t, r, "f1", "ns")

	assert.Zero(t, keyCount(t, kv, "ns", "carts"), "keyspace purged")
	got := &fv1.Function{}
	err := c.Get(t.Context(), types.NamespacedName{Name: "f1", Namespace: "ns"}, got)
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil, "finalizer released, object gone")
	assert.False(t, r.index.Known("ns", "carts"))
}

func TestReconcilerRetainAnnotationSkipsPurge(t *testing.T) {
	t.Parallel()
	fn := stateFn("f1", "ns", &fv1.StateConfig{Keyspace: "keepme"})
	fn.Finalizers = []string{stateFinalizer}
	fn.Annotations = map[string]string{AnnotationStateRetain: "true"}
	r, c, kv := newTestReconciler(t, fn)
	reconcile(t, r, "f1", "ns")
	seedKeys(t, kv, "ns", "keepme", "a", "b")

	require.NoError(t, c.Delete(t.Context(), fn))
	reconcile(t, r, "f1", "ns")

	assert.Equal(t, 2, keyCount(t, kv, "ns", "keepme"), "retained data survives delete")
}

func TestReconcilerSharedKeyspaceSkipsPurge(t *testing.T) {
	t.Parallel()
	f1 := stateFn("f1", "ns", &fv1.StateConfig{Keyspace: "shared"})
	f1.Finalizers = []string{stateFinalizer}
	f2 := stateFn("f2", "ns", &fv1.StateConfig{Keyspace: "shared"})
	r, c, kv := newTestReconciler(t, f1, f2)
	reconcile(t, r, "f1", "ns")
	reconcile(t, r, "f2", "ns")
	seedKeys(t, kv, "ns", "shared", "a")

	require.NoError(t, c.Delete(t.Context(), f1))
	reconcile(t, r, "f1", "ns")

	assert.Equal(t, 1, keyCount(t, kv, "ns", "shared"), "still claimed by f2: not purged")
	assert.True(t, r.index.Known("ns", "shared"))
}

// erroringKV wraps a KVStore and fails List, to exercise the purge-failure
// path that must KEEP the finalizer (never silently orphan keyspace data).
type erroringKV struct {
	statestore.KVStore
	listErr error
}

func (e erroringKV) List(ctx context.Context, s statestore.Scope, prefix string, page statestore.Page) (statestore.KeyPage, error) {
	return statestore.KeyPage{}, e.listErr
}

func TestReconcilerPurgeFailureKeepsFinalizer(t *testing.T) {
	t.Parallel()
	fn := stateFn("f1", "ns", &fv1.StateConfig{Keyspace: "carts"})
	fn.Finalizers = []string{stateFinalizer}
	r, c, kv := newTestReconciler(t, fn)
	reconcile(t, r, "f1", "ns")
	seedKeys(t, kv, "ns", "carts", "a")
	r.kv = erroringKV{KVStore: kv, listErr: errors.New("statestore unavailable")}

	require.NoError(t, c.Delete(t.Context(), fn))
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "f1", Namespace: "ns"}})
	require.Error(t, err, "purge failure must surface so the delete retries")

	got := &fv1.Function{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "f1", Namespace: "ns"}, got))
	assert.Contains(t, got.Finalizers, stateFinalizer, "finalizer retained so data is never silently orphaned")
}

func TestReconcilerNotFoundDropsIndexEntry(t *testing.T) {
	t.Parallel()
	r, _, _ := newTestReconciler(t)
	r.index.Upsert(types.NamespacedName{Name: "gone", Namespace: "ns"}, &fv1.StateConfig{})
	reconcile(t, r, "gone", "ns")
	assert.False(t, r.index.Known("ns", "gone"))
}
