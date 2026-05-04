//go:build integration

package framework

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CreateConfigMap creates a ConfigMap in the test namespace with the given
// string-data entries and registers its deletion on the namespace cleanup
// chain. Mirrors `kubectl create configmap <name> --from-literal=k=v ...`.
func (ns *TestNamespace) CreateConfigMap(t *testing.T, ctx context.Context, name string, data map[string]string) {
	t.Helper()
	require.NotEmpty(t, name, "CreateConfigMap: name")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
		Data:       data,
	}
	_, err := ns.f.kubeClient.CoreV1().ConfigMaps(ns.Name).Create(ctx, cm, metav1.CreateOptions{})
	require.NoErrorf(t, err, "create configmap %q", name)
	ns.addCleanup("configmap "+name, func(c context.Context) error {
		err := ns.f.kubeClient.CoreV1().ConfigMaps(ns.Name).Delete(c, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}

// CreateSecret creates an Opaque Secret in the test namespace with the given
// string-data entries and registers its deletion on the namespace cleanup
// chain. Mirrors `kubectl create secret generic <name> --from-literal=k=v ...`.
func (ns *TestNamespace) CreateSecret(t *testing.T, ctx context.Context, name string, data map[string]string) {
	t.Helper()
	require.NotEmpty(t, name, "CreateSecret: name")
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
		StringData: data,
		Type:       corev1.SecretTypeOpaque,
	}
	_, err := ns.f.kubeClient.CoreV1().Secrets(ns.Name).Create(ctx, sec, metav1.CreateOptions{})
	require.NoErrorf(t, err, "create secret %q", name)
	ns.addCleanup("secret "+name, func(c context.Context) error {
		err := ns.f.kubeClient.CoreV1().Secrets(ns.Name).Delete(c, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}
