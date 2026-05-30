// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cms

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/executortype"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// fakeExecutor satisfies executortype.ExecutorType by embedding the interface
// (the other methods are never called in these tests) and only implements
// RefreshFuncPods, recording invocations.
type fakeExecutor struct {
	executortype.ExecutorType
	refreshCount int
	refreshErr   error
}

func (f *fakeExecutor) RefreshFuncPods(context.Context, logr.Logger, fv1.Function) error {
	f.refreshCount++
	return f.refreshErr
}

const cmsNamespace = "ns1"

func functionRefingConfigMap(name, cmName string) fv1.Function {
	fn := fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cmsNamespace}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypePoolmgr
	fn.Spec.ConfigMaps = []fv1.ConfigMapReference{{Name: cmName, Namespace: cmsNamespace}}
	return fn
}

func functionRefingSecret(name, secretName string) fv1.Function {
	fn := fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cmsNamespace}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypePoolmgr
	fn.Spec.Secrets = []fv1.SecretReference{{Name: secretName, Namespace: cmsNamespace}}
	return fn
}

func TestGetConfigmapRelatedFuncs(t *testing.T) {
	t.Parallel()
	related := functionRefingConfigMap("fn-related", "cfg")
	unrelated := functionRefingConfigMap("fn-unrelated", "other")
	client := fClient.NewClientset(&related, &unrelated)

	meta := &metav1.ObjectMeta{Name: "cfg", Namespace: cmsNamespace}
	funcs, err := getConfigmapRelatedFuncs(t.Context(), meta, client)
	require.NoError(t, err)
	require.Len(t, funcs, 1)
	assert.Equal(t, "fn-related", funcs[0].Name)

	none, err := getConfigmapRelatedFuncs(t.Context(), &metav1.ObjectMeta{Name: "nope", Namespace: cmsNamespace}, client)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestGetSecretRelatedFuncs(t *testing.T) {
	t.Parallel()
	related := functionRefingSecret("fn-related", "sec")
	unrelated := functionRefingSecret("fn-unrelated", "other")
	client := fClient.NewClientset(&related, &unrelated)

	meta := &metav1.ObjectMeta{Name: "sec", Namespace: cmsNamespace}
	funcs, err := getSecretRelatedFuncs(t.Context(), logr.Discard(), meta, client)
	require.NoError(t, err)
	require.Len(t, funcs, 1)
	assert.Equal(t, "fn-related", funcs[0].Name)
}

func TestRefreshPods(t *testing.T) {
	t.Run("invokes the executor for a known type", func(t *testing.T) {
		exec := &fakeExecutor{}
		types := map[fv1.ExecutorType]executortype.ExecutorType{fv1.ExecutorTypePoolmgr: exec}
		refreshPods(t.Context(), logr.Discard(), []fv1.Function{functionRefingConfigMap("fn", "cfg")}, types)
		assert.Equal(t, 1, exec.refreshCount)
	})

	t.Run("logs but does not panic when the executor errors", func(t *testing.T) {
		exec := &fakeExecutor{refreshErr: errors.New("boom")}
		types := map[fv1.ExecutorType]executortype.ExecutorType{fv1.ExecutorTypePoolmgr: exec}
		refreshPods(t.Context(), logr.Discard(), []fv1.Function{functionRefingConfigMap("fn", "cfg")}, types)
		assert.Equal(t, 1, exec.refreshCount)
	})

	t.Run("unknown executor type is handled", func(t *testing.T) {
		refreshPods(t.Context(), logr.Discard(), []fv1.Function{functionRefingConfigMap("fn", "cfg")},
			map[fv1.ExecutorType]executortype.ExecutorType{})
	})
}

func configMap(name, rv string) *apiv1.ConfigMap {
	return &apiv1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cmsNamespace, ResourceVersion: rv}}
}

func req(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: cmsNamespace}}
}

func TestConfigMapReconciler(t *testing.T) {
	related := functionRefingConfigMap("fn", "cfg")
	exec := &fakeExecutor{}
	r := &ConfigMapReconciler{
		logger:        logr.Discard(),
		client:        crfake.NewClientBuilder().WithObjects(configMap("cfg", "2"), configMap("unreferenced", "1")).Build(),
		fissionClient: fClient.NewClientset(&related),
		types:         map[fv1.ExecutorType]executortype.ExecutorType{fv1.ExecutorTypePoolmgr: exec},
	}

	t.Run("referenced configmap recycles the function's pods", func(t *testing.T) {
		_, err := r.Reconcile(t.Context(), req("cfg"))
		require.NoError(t, err)
		assert.Equal(t, 1, exec.refreshCount)
	})

	t.Run("unreferenced configmap is a no-op", func(t *testing.T) {
		before := exec.refreshCount
		_, err := r.Reconcile(t.Context(), req("unreferenced"))
		require.NoError(t, err)
		assert.Equal(t, before, exec.refreshCount)
	})

	t.Run("deleted configmap is a no-op", func(t *testing.T) {
		before := exec.refreshCount
		_, err := r.Reconcile(t.Context(), req("gone"))
		require.NoError(t, err)
		assert.Equal(t, before, exec.refreshCount)
	})
}

func secret(name, rv string) *apiv1.Secret {
	return &apiv1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cmsNamespace, ResourceVersion: rv}}
}

func TestSecretReconciler(t *testing.T) {
	related := functionRefingSecret("fn", "sec")
	exec := &fakeExecutor{}
	r := &SecretReconciler{
		logger:        logr.Discard(),
		client:        crfake.NewClientBuilder().WithObjects(secret("sec", "2")).Build(),
		fissionClient: fClient.NewClientset(&related),
		types:         map[fv1.ExecutorType]executortype.ExecutorType{fv1.ExecutorTypePoolmgr: exec},
	}
	_, err := r.Reconcile(t.Context(), req("sec"))
	require.NoError(t, err)
	assert.Equal(t, 1, exec.refreshCount)
}

// TestContentChangedPredicate pins the predicate that reproduces the old
// handlers: only a ResourceVersion-changing Update enqueues; Create/Delete are
// dropped (so the startup list of every ConfigMap/Secret doesn't refresh pods).
func TestContentChangedPredicate(t *testing.T) {
	p := contentChangedPredicate()
	assert.False(t, p.Create(event.CreateEvent{Object: configMap("cfg", "1")}), "create dropped")
	assert.False(t, p.Delete(event.DeleteEvent{Object: configMap("cfg", "1")}), "delete dropped")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: configMap("cfg", "1"), ObjectNew: configMap("cfg", "2")}),
		"content change enqueues")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: configMap("cfg", "1"), ObjectNew: configMap("cfg", "1")}),
		"same resource version dropped")
}
