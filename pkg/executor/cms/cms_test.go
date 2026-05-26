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
	"k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

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

func TestConfigMapEventHandlers(t *testing.T) {
	related := functionRefingConfigMap("fn", "cfg")
	client := fClient.NewClientset(&related)
	exec := &fakeExecutor{}
	types := map[fv1.ExecutorType]executortype.ExecutorType{fv1.ExecutorTypePoolmgr: exec}
	h := ConfigMapEventHandlers(t.Context(), logr.Discard(), client, k8sfake.NewClientset(), types)

	// Add/Delete are intentional no-ops.
	h.OnAdd(configMap("cfg", "1"), false)
	h.OnDelete(configMap("cfg", "1"))
	assert.Equal(t, 0, exec.refreshCount)

	t.Run("same resource version does not refresh", func(t *testing.T) {
		h.OnUpdate(configMap("cfg", "1"), configMap("cfg", "1"))
		assert.Equal(t, 0, exec.refreshCount)
	})

	t.Run("changed configmap with related funcs refreshes", func(t *testing.T) {
		h.OnUpdate(configMap("cfg", "1"), configMap("cfg", "2"))
		assert.Equal(t, 1, exec.refreshCount)
	})

	t.Run("changed configmap with no related funcs is a no-op", func(t *testing.T) {
		before := exec.refreshCount
		h.OnUpdate(configMap("unreferenced", "1"), configMap("unreferenced", "2"))
		assert.Equal(t, before, exec.refreshCount)
	})
}

func secret(name, rv string) *apiv1.Secret {
	return &apiv1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cmsNamespace, ResourceVersion: rv}}
}

func TestSecretEventHandlers(t *testing.T) {
	related := functionRefingSecret("fn", "sec")
	client := fClient.NewClientset(&related)
	exec := &fakeExecutor{}
	types := map[fv1.ExecutorType]executortype.ExecutorType{fv1.ExecutorTypePoolmgr: exec}
	h := SecretEventHandlers(t.Context(), logr.Discard(), client, k8sfake.NewClientset(), types)

	h.OnUpdate(secret("sec", "1"), secret("sec", "1"))
	assert.Equal(t, 0, exec.refreshCount)

	h.OnUpdate(secret("sec", "1"), secret("sec", "2"))
	assert.Equal(t, 1, exec.refreshCount)
}

func TestMakeConfigSecretController(t *testing.T) {
	factory := informers.NewSharedInformerFactory(k8sfake.NewClientset(), 0)
	cmInformer := map[string]cache.SharedIndexInformer{cmsNamespace: factory.Core().V1().ConfigMaps().Informer()}
	secretInformer := map[string]cache.SharedIndexInformer{cmsNamespace: factory.Core().V1().Secrets().Informer()}

	ctrl, err := MakeConfigSecretController(t.Context(), logr.Discard(), fClient.NewClientset(), k8sfake.NewClientset(),
		map[fv1.ExecutorType]executortype.ExecutorType{}, cmInformer, secretInformer)
	require.NoError(t, err)
	assert.NotNil(t, ctrl)
}
