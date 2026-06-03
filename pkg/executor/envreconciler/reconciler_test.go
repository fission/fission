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
	name      string
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
		h1 := &fakeHandler{name: "poolmgr", requeue: 30 * time.Minute}
		h2 := &fakeHandler{name: "newdeploy"}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:1")), handlers: []executortype.EnvReconciler{h1, h2}}

		res, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, [][2]string{{"", "img:1"}}, h1.reconciled)
		assert.Equal(t, [][2]string{{"", "img:1"}}, h2.reconciled)
		assert.Equal(t, 30*time.Minute, res.RequeueAfter, "requeue is the longest any handler requests")
		_, cached := r.lastSeen.Load(key)
		assert.True(t, cached)
	})

	t.Run("second event hands each handler the cached old object", func(t *testing.T) {
		h := &fakeHandler{name: "newdeploy"}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:2")), handlers: []executortype.EnvReconciler{h}}
		r.lastSeen.Store(key, envWithImage("env", "img:1"))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, [][2]string{{"img:1", "img:2"}}, h.reconciled, "handler must see the previous image as old")
		cached, _ := r.lastSeen.Load(key)
		assert.Equal(t, "img:2", cached.(*fv1.Environment).Spec.Runtime.Image, "cache advances to the new object")
	})

	t.Run("a handler error stops dispatch and does not advance the cache", func(t *testing.T) {
		h1 := &fakeHandler{name: "poolmgr", reconcErr: assert.AnError}
		h2 := &fakeHandler{name: "newdeploy"}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:1")), handlers: []executortype.EnvReconciler{h1, h2}}

		_, err := r.Reconcile(t.Context(), req)
		require.Error(t, err)
		assert.Empty(t, h2.reconciled, "dispatch stops at the first failing handler")
		_, cached := r.lastSeen.Load(key)
		assert.False(t, cached, "a retry must hand handlers the same old object, so the cache is not advanced")
	})

	t.Run("delete dispatches cleanup to every handler with the cached object", func(t *testing.T) {
		h1 := &fakeHandler{name: "poolmgr"}
		h2 := &fakeHandler{name: "newdeploy"}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(), handlers: []executortype.EnvReconciler{h1, h2}} // empty client -> NotFound
		r.lastSeen.Store(key, envWithImage("env", "img:1"))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"env"}, h1.cleaned)
		assert.Equal(t, []string{"env"}, h2.cleaned)
		_, cached := r.lastSeen.Load(key)
		assert.False(t, cached)
	})

	t.Run("delete of an unseen environment is a no-op", func(t *testing.T) {
		h := &fakeHandler{name: "poolmgr"}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(), handlers: []executortype.EnvReconciler{h}}

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, h.cleaned)
	})
}

// fakeExecutorType implements only the bits collectHandlers needs: it optionally
// embeds an EnvReconciler. envType reports whether it reacts to Environments.
type fakeExecutorType struct {
	executortype.ExecutorType // nil; never called by collectHandlers
	envType                   bool
}

func (f fakeExecutorType) ReconcileEnvironment(context.Context, *fv1.Environment, *fv1.Environment) (time.Duration, error) {
	return 0, nil
}
func (f fakeExecutorType) CleanupEnvironment(context.Context, *fv1.Environment) {}

// nonEnvExecutorType implements ExecutorType but NOT EnvReconciler.
type nonEnvExecutorType struct{ executortype.ExecutorType }

func TestCollectHandlers(t *testing.T) {
	t.Run("only types implementing EnvReconciler are collected, in name order", func(t *testing.T) {
		types := map[fv1.ExecutorType]executortype.ExecutorType{
			"poolmgr":   fakeExecutorType{envType: true},
			"newdeploy": fakeExecutorType{envType: true},
			"container": nonEnvExecutorType{},
		}
		handlers := collectHandlers(types)
		assert.Len(t, handlers, 2, "container does not implement EnvReconciler and is skipped")
	})

	t.Run("no env-reacting types yields no handlers", func(t *testing.T) {
		types := map[fv1.ExecutorType]executortype.ExecutorType{
			"container": nonEnvExecutorType{},
		}
		assert.Empty(t, collectHandlers(types))
	})
}
