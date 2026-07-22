// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package envreconciler

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

// fakeHandler records the (old image, new image) pairs it reconciles and the
// names it cleans up. It satisfies executortype.EnvReconciler.
type fakeHandler struct {
	requeue   time.Duration
	reconcErr error

	reconciled [][2]string // {oldImage, newImage}; oldImage == "" means old was nil
	cleaned    []string
}

func (f *fakeHandler) ReconcileEnvironment(_ context.Context, old, env *fv1.Environment) (time.Duration, error) {
	oldImg := ""
	if old != nil {
		oldImg = old.Spec.Runtime.Image
	}
	f.reconciled = append(f.reconciled, [2]string{oldImg, env.Spec.Runtime.Image})
	return f.requeue, f.reconcErr
}

func (f *fakeHandler) CleanupEnvironment(_ context.Context, env *fv1.Environment) {
	f.cleaned = append(f.cleaned, env.Name)
}

func crClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
}

func envWithImage(name, image string) *fv1.Environment {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "e1"}}
	env.Spec.Runtime.Image = image
	return env
}

func TestEnvironmentReconcilerDispatch(t *testing.T) {
	key := types.NamespacedName{Name: "env", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	t.Run("first sight dispatches to every handler with nil old and caches", func(t *testing.T) {
		h1 := &fakeHandler{requeue: 30 * time.Minute}
		h2 := &fakeHandler{}
		c := crClient(envWithImage("env", "img:1"))
		r := &environmentReconciler{logger: logr.Discard(), client: c, apiReader: c, handlers: []executortype.EnvReconciler{h1, h2}}

		res, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, [][2]string{{"", "img:1"}}, h1.reconciled)
		assert.Equal(t, [][2]string{{"", "img:1"}}, h2.reconciled)
		assert.Equal(t, 30*time.Minute, res.RequeueAfter, "requeue is the longest any handler requests")
		_, cached := r.lastSeen.Load(key)
		assert.True(t, cached)
	})

	t.Run("second event hands each handler the cached old object", func(t *testing.T) {
		h := &fakeHandler{}
		c := crClient(envWithImage("env", "img:2"))
		r := &environmentReconciler{logger: logr.Discard(), client: c, apiReader: c, handlers: []executortype.EnvReconciler{h}}
		r.lastSeen.Store(key, envWithImage("env", "img:1"))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, [][2]string{{"img:1", "img:2"}}, h.reconciled, "handler must see the previous image as old")
		cached, _ := r.lastSeen.Load(key)
		assert.Equal(t, "img:2", cached.(*fv1.Environment).Spec.Runtime.Image, "cache advances to the new object")
	})

	t.Run("a handler error stops dispatch and does not advance the cache", func(t *testing.T) {
		h1 := &fakeHandler{reconcErr: assert.AnError}
		h2 := &fakeHandler{}
		c := crClient(envWithImage("env", "img:1"))
		r := &environmentReconciler{logger: logr.Discard(), client: c, apiReader: c, handlers: []executortype.EnvReconciler{h1, h2}}

		_, err := r.Reconcile(t.Context(), req)
		require.Error(t, err)
		assert.Empty(t, h2.reconciled, "dispatch stops at the first failing handler")
		_, cached := r.lastSeen.Load(key)
		assert.False(t, cached, "a retry must hand handlers the same old object, so the cache is not advanced")
	})

	t.Run("delete dispatches cleanup to every handler with the cached object", func(t *testing.T) {
		h1 := &fakeHandler{}
		h2 := &fakeHandler{}
		c := crClient() // empty client -> NotFound from both cache and API reader
		r := &environmentReconciler{logger: logr.Discard(), client: c, apiReader: c, handlers: []executortype.EnvReconciler{h1, h2}}
		r.lastSeen.Store(key, envWithImage("env", "img:1"))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"env"}, h1.cleaned)
		assert.Equal(t, []string{"env"}, h2.cleaned)
		_, cached := r.lastSeen.Load(key)
		assert.False(t, cached)
	})

	t.Run("delete of an unseen environment is a no-op", func(t *testing.T) {
		h := &fakeHandler{}
		c := crClient()
		r := &environmentReconciler{logger: logr.Discard(), client: c, apiReader: c, handlers: []executortype.EnvReconciler{h}}

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, h.cleaned)
	})

	t.Run("stale cache NotFound does not trigger cleanup when env exists in API", func(t *testing.T) {
		h := &fakeHandler{}
		// Simulate stale cache: client (cache) is empty -> NotFound,
		// but apiReader has the env -> exists. Use two different fake clients.
		cacheClient := crClient() // empty -> NotFound from cache
		apiClient := crClient(envWithImage("env", "img:1"))
		r := &environmentReconciler{logger: logr.Discard(), client: cacheClient, apiReader: apiClient, handlers: []executortype.EnvReconciler{h}}
		r.lastSeen.Store(key, envWithImage("env", "img:1"))

		res, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.NotZero(t, res.RequeueAfter, "should requeue to retry with fresh cache")
		assert.Empty(t, h.cleaned, "cleanup must NOT fire when env still exists in API server")
		cached, _ := r.lastSeen.Load(key)
		assert.NotNil(t, cached, "lastSeen should be refreshed from API server")
	})
}

// fakeEnvExecutorType is an ExecutorType that also implements EnvReconciler. It
// embeds a nil ExecutorType (collectHandlers never calls those methods); id lets
// the ordering assertion identify which type a collected handler is.
type fakeEnvExecutorType struct {
	executortype.ExecutorType
	id string
}

func (fakeEnvExecutorType) ReconcileEnvironment(context.Context, *fv1.Environment, *fv1.Environment) (time.Duration, error) {
	return 0, nil
}
func (fakeEnvExecutorType) CleanupEnvironment(context.Context, *fv1.Environment) {}

// nonEnvExecutorType implements ExecutorType but NOT EnvReconciler.
type nonEnvExecutorType struct{ executortype.ExecutorType }

func TestCollectHandlers(t *testing.T) {
	t.Run("collects only EnvReconciler types, ordered by executor-type name", func(t *testing.T) {
		types := map[fv1.ExecutorType]executortype.ExecutorType{
			"poolmgr":   fakeEnvExecutorType{id: "poolmgr"},
			"newdeploy": fakeEnvExecutorType{id: "newdeploy"},
			"container": nonEnvExecutorType{}, // not an EnvReconciler -> skipped
		}
		handlers := collectHandlers(types)
		require.Len(t, handlers, 2, "container does not implement EnvReconciler and is skipped")
		// Deterministic, sorted by executor-type name: newdeploy < poolmgr.
		assert.Equal(t, "newdeploy", handlers[0].(fakeEnvExecutorType).id)
		assert.Equal(t, "poolmgr", handlers[1].(fakeEnvExecutorType).id)
	})

	t.Run("no env-reacting types yields no handlers", func(t *testing.T) {
		types := map[fv1.ExecutorType]executortype.ExecutorType{
			"container": nonEnvExecutorType{},
		}
		assert.Empty(t, collectHandlers(types))
	})
}
