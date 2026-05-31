// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

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
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

type fakeDeleter struct{ deleted []crd.CacheKeyURG }

func (f *fakeDeleter) markFuncDeleted(k crd.CacheKeyURG) { f.deleted = append(f.deleted, k) }

func crClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
}

func TestPoolmgrFunctionReconciler(t *testing.T) {
	key := types.NamespacedName{Name: "fn", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "u1", ResourceVersion: "9", Generation: 2}}

	t.Run("existing function is cached, not marked deleted", func(t *testing.T) {
		d := &fakeDeleter{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fn), deleter: d}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, d.deleted)
		_, cached := r.lastSeen.Load(key)
		assert.True(t, cached, "live function must be cached so its URG is available on delete")
	})

	t.Run("deleted function is marked deleted with its cached URG", func(t *testing.T) {
		d := &fakeDeleter{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(), deleter: d} // empty client -> NotFound
		r.lastSeen.Store(key, fn.DeepCopy())
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		require.Len(t, d.deleted, 1)
		assert.Equal(t, crd.CacheKeyURGFromMeta(&fn.ObjectMeta), d.deleted[0])
		_, cached := r.lastSeen.Load(key)
		assert.False(t, cached)
	})

	t.Run("delete of an unseen function is a no-op", func(t *testing.T) {
		d := &fakeDeleter{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(), deleter: d}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, d.deleted)
	})

	t.Run("delete+recreate with a new UID marks the old UID deleted", func(t *testing.T) {
		d := &fakeDeleter{}
		recreated := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "u2", ResourceVersion: "3", Generation: 1}}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(recreated), deleter: d}
		r.lastSeen.Store(key, fn.DeepCopy()) // old UID u1
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		require.Len(t, d.deleted, 1)
		assert.Equal(t, crd.CacheKeyURGFromMeta(&fn.ObjectMeta), d.deleted[0], "old UID must be marked deleted")
		cached, _ := r.lastSeen.Load(key)
		assert.Equal(t, recreated.UID, cached.(*fv1.Function).UID, "cache now holds the recreated function")
	})
}
