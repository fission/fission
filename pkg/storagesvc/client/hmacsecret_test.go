// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func executorDeploymentWithSecretName(ns, name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "executor", Namespace: ns},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "executor",
				Env:  []corev1.EnvVar{{Name: fv1.InternalAuthSecretNameEnv, Value: name}},
			}},
		}}},
	}
}

func masterSecret(ns, name, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       map[string][]byte{internalAuthSecretKey: []byte(value)},
	}
}

// TestHMACSecretFromClusterDiscoversNonDefaultName pins the
// internalAuth.existingSecret CLI contract: with the install pointed at a
// non-default Secret, the CLI (which never has the pod-only name env) must
// discover the name from the cluster instead of looking up the default,
// getting NotFound, and silently sending unsigned requests that 401.
func TestHMACSecretFromClusterDiscoversNonDefaultName(t *testing.T) {
	const ns = "fission"

	t.Run("discovers the name the cluster was installed with", func(t *testing.T) {
		kube := fake.NewClientset(
			executorDeploymentWithSecretName(ns, "org-auth-master"),
			masterSecret(ns, "org-auth-master", "0123456789abcdef0123456789abcdef"),
		)
		got, err := HMACSecretFromCluster(t.Context(), kube, ns)
		require.NoError(t, err)
		assert.Equal(t, []byte("0123456789abcdef0123456789abcdef"), got,
			"the existingSecret name stamped on the executor Deployment must be used")
	})

	t.Run("explicit env override beats cluster discovery", func(t *testing.T) {
		t.Setenv(fv1.InternalAuthSecretNameEnv, "operator-pinned")
		kube := fake.NewClientset(
			executorDeploymentWithSecretName(ns, "org-auth-master"),
			masterSecret(ns, "operator-pinned", "fedcba9876543210fedcba9876543210"),
		)
		got, err := HMACSecretFromCluster(t.Context(), kube, ns)
		require.NoError(t, err)
		assert.Equal(t, []byte("fedcba9876543210fedcba9876543210"), got)
	})

	t.Run("no executor deployment falls back to the default name", func(t *testing.T) {
		kube := fake.NewClientset(
			masterSecret(ns, fv1.DefaultInternalAuthSecret, "00112233445566770011223344556677"),
		)
		got, err := HMACSecretFromCluster(t.Context(), kube, ns)
		require.NoError(t, err)
		assert.Equal(t, []byte("00112233445566770011223344556677"), got)
	})

	t.Run("auth disabled (no secret anywhere) stays a clean unsigned fallback", func(t *testing.T) {
		kube := fake.NewClientset(executorDeploymentWithSecretName(ns, "org-auth-master"))
		got, err := HMACSecretFromCluster(t.Context(), kube, ns)
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}
