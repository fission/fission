// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func newTestContainer() *Container {
	return &Container{
		logger:                 loggerfactory.GetLogger(),
		kubernetesClient:       fake.NewClientset(),
		runtimeImagePullPolicy: apiv1.PullIfNotPresent,
	}
}

func newTestContainerFunction() *fv1.Function {
	grace := int64(30)
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "ctr-fn", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypeContainer, MinScale: 1, MaxScale: 3},
			},
			PodSpec: &apiv1.PodSpec{
				TerminationGracePeriodSeconds: &grace,
				Containers: []apiv1.Container{
					{Name: "ctr-fn", Image: "example/app:latest"},
				},
			},
		},
	}
}

func findContainer(pod apiv1.PodSpec, name string) *apiv1.Container {
	for i := range pod.Containers {
		if pod.Containers[i].Name == name {
			return &pod.Containers[i]
		}
	}
	return nil
}

func TestContainerGetDeploymentSpec(t *testing.T) {
	t.Run("builds a deployment from the function pod spec", func(t *testing.T) {
		cn := newTestContainer()
		fn := newTestContainerFunction()
		replicas := int32(1)

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, &replicas, "ctr-fn", "default",
			map[string]string{"app": "ctr"}, map[string]string{"note": "x"})
		require.NoError(t, err)

		assert.Equal(t, "ctr-fn", deployment.Name)
		assert.Equal(t, map[string]string{"app": "ctr"}, deployment.Labels)
		assert.Equal(t, map[string]string{"note": "x"}, deployment.Annotations)
		require.NotNil(t, deployment.Spec.Replicas)
		assert.Equal(t, int32(1), *deployment.Spec.Replicas)
		assert.Equal(t, map[string]string{"app": "ctr"}, deployment.Spec.Selector.MatchLabels)
		require.NotNil(t, deployment.Spec.RevisionHistoryLimit)
		assert.Equal(t, int32(0), *deployment.Spec.RevisionHistoryLimit)
		assert.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, deployment.Spec.Strategy.Type)

		pod := deployment.Spec.Template.Spec
		ctr := findContainer(pod, "ctr-fn")
		require.NotNil(t, ctr, "user container must be present")
		assert.Equal(t, "example/app:latest", ctr.Image)
		assert.Equal(t, apiv1.PullIfNotPresent, ctr.ImagePullPolicy)
		require.NotNil(t, pod.TerminationGracePeriodSeconds)
		assert.Equal(t, int64(30), *pod.TerminationGracePeriodSeconds)
	})

	t.Run("nil targetReplicas falls back to MinScale", func(t *testing.T) {
		cn := newTestContainer()
		fn := newTestContainerFunction()
		fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale = 2

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, nil, "ctr-fn", "default", nil, nil)
		require.NoError(t, err)
		require.NotNil(t, deployment.Spec.Replicas)
		assert.Equal(t, int32(2), *deployment.Spec.Replicas)
	})

	t.Run("istio disables sidecar injection", func(t *testing.T) {
		cn := newTestContainer()
		cn.useIstio = true
		fn := newTestContainerFunction()
		replicas := int32(1)

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, &replicas, "ctr-fn", "default", nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "false", deployment.Spec.Template.Annotations["sidecar.istio.io/inject"])
	})

	t.Run("owner references are added when enabled", func(t *testing.T) {
		cn := newTestContainer()
		cn.enableOwnerReferences = true
		fn := newTestContainerFunction()
		replicas := int32(1)

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, &replicas, "ctr-fn", "default", nil, nil)
		require.NoError(t, err)
		require.Len(t, deployment.OwnerReferences, 1)
		assert.Equal(t, "Function", deployment.OwnerReferences[0].Kind)
		assert.Equal(t, "ctr-fn", deployment.OwnerReferences[0].Name)
	})
}

func TestContainerGetResources(t *testing.T) {
	cn := newTestContainer()
	fn := newTestContainerFunction()
	// no resource requests/limits set -> maps initialised, not nil
	res := cn.getResources(fn)
	assert.NotNil(t, res.Requests)
	assert.NotNil(t, res.Limits)
}
