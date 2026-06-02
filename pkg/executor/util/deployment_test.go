// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func secret(ns, name, rv string) *apiv1.Secret {
	return &apiv1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, ResourceVersion: rv}}
}

func configMap(ns, name, rv string) *apiv1.ConfigMap {
	return &apiv1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, ResourceVersion: rv}}
}

func secretRef(ns, name string) fv1.SecretReference {
	return fv1.SecretReference{Namespace: ns, Name: name}
}

func configMapRef(ns, name string) fv1.ConfigMapReference {
	return fv1.ConfigMapReference{Namespace: ns, Name: name}
}

func TestReferencedResourcesRVSum(t *testing.T) {
	t.Parallel()

	const ns = "default"

	tests := []struct {
		name    string
		objects []runtime.Object
		secrets []fv1.SecretReference
		cfgmaps []fv1.ConfigMapReference
		want    int
	}{
		{
			name:    "sums referenced secret and configmap resource versions",
			objects: []runtime.Object{secret(ns, "s1", "10"), configMap(ns, "c1", "20")},
			secrets: []fv1.SecretReference{secretRef(ns, "s1")},
			cfgmaps: []fv1.ConfigMapReference{configMapRef(ns, "c1")},
			want:    30,
		},
		{
			name: "ignores unreferenced objects in the namespace",
			objects: []runtime.Object{
				secret(ns, "s1", "10"), secret(ns, "s2", "99"),
				configMap(ns, "c1", "20"), configMap(ns, "c2", "77"),
			},
			secrets: []fv1.SecretReference{secretRef(ns, "s1")},
			cfgmaps: []fv1.ConfigMapReference{configMapRef(ns, "c1")},
			want:    30,
		},
		{
			name:    "empty references return zero without listing",
			objects: []runtime.Object{secret(ns, "s1", "10")},
			want:    0,
		},
		{
			name:    "missing referenced object contributes zero and does not error",
			objects: []runtime.Object{secret(ns, "s1", "10")},
			secrets: []fv1.SecretReference{secretRef(ns, "s1"), secretRef(ns, "ghost")},
			want:    10,
		},
		{
			name:    "cross-namespace reference is skipped",
			objects: []runtime.Object{secret("other", "s1", "10")},
			secrets: []fv1.SecretReference{secretRef("other", "s1")},
			want:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := fake.NewClientset(tc.objects...)
			got, err := ReferencedResourcesRVSum(t.Context(), client, ns, tc.secrets, tc.cfgmaps)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestScaleDeployment(t *testing.T) {
	t.Parallel()

	const ns, name = "default", "dep1"
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
	client := fake.NewClientset(dep)

	// Capture the scale subresource update directly: the fake clientset's
	// GetScale read-back has a known scheme-conversion bug, so we assert on
	// the object the helper sends rather than reading it back.
	var captured *autoscalingv1.Scale
	client.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "scale" {
			return false, nil, nil
		}
		obj := action.(k8stesting.UpdateAction).GetObject()
		captured = obj.(*autoscalingv1.Scale)
		return true, obj, nil
	})

	err := ScaleDeployment(t.Context(), client, loggerfactory.GetLogger(), ns, name, 3)
	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, int32(3), captured.Spec.Replicas)
}

func TestWaitForDeployment(t *testing.T) {
	t.Parallel()

	const ns = "default"

	t.Run("returns once available replicas meet the target", func(t *testing.T) {
		t.Parallel()
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ready"},
			Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
		}
		client := fake.NewClientset(dep)

		// AvailableReplicas already satisfies the target, so the poll loop
		// returns on the first iteration without sleeping. The wall-clock
		// timeout path (floored at fv1.DefaultSpecializationTimeOut) is left
		// to integration coverage so the unit test stays fast.
		got, err := WaitForDeployment(t.Context(), client, loggerfactory.GetLogger(), dep, 1, 1)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.GreaterOrEqual(t, got.Status.AvailableReplicas, int32(1))
	})

	t.Run("surfaces a get error", func(t *testing.T) {
		t.Parallel()
		client := fake.NewClientset()
		missing := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ghost"}}

		got, err := WaitForDeployment(t.Context(), client, loggerfactory.GetLogger(), missing, 1, 1)
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("returns promptly when the context is cancelled", func(t *testing.T) {
		t.Parallel()
		// Deployment exists but is not yet available, so the loop would
		// otherwise sleep; a cancelled context must short-circuit instead of
		// blocking for the (floored) timeout window.
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "pending"},
			Status:     appsv1.DeploymentStatus{AvailableReplicas: 0},
		}
		client := fake.NewClientset(dep)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		got, err := WaitForDeployment(ctx, client, loggerfactory.GetLogger(), dep, 1, 1)
		require.Error(t, err)
		assert.Nil(t, got)
	})
}
