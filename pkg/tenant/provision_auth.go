// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/auth/hmac"
)

// keysSecretName is the controller-owned per-namespace derived-key Secret. It is
// a DIFFERENT name from the chart's master-bearing "fission-internal-auth" so the
// controller can create it cleanly in a namespace that already holds the chart's
// master copy (an existing install replicated one into every function namespace);
// a same-named Secret would collide on create and silently never write the keys.
// Sharing the name+field constants with the fetcher pod-spec keeps writer and
// reader in lockstep.
const (
	keysSecretName  = fv1.TenantAuthKeysSecret
	fetcherKeyField = fv1.TenantAuthFetcherKey
	builderKeyField = fv1.TenantAuthBuilderKey
	storageKeyField = fv1.TenantAuthStorageKey
)

// NamespaceAuthSecret builds the internal-auth Secret for a tenant namespace
// holding ONLY the namespace-scoped derived keys — never the master. A leak of
// this Secret yields keys that can act as this tenant on the fetcher/builder/
// storagesvc channels and nothing else: not as another tenant, and not as the
// master-derived control-plane channels (executor, router-internal). This is the
// security upgrade over the chart's current master-per-namespace copy.
//
// Returns nil for an empty master (internalAuth disabled); the caller then skips
// writing it, matching hmac.DeriveServiceKey's empty-master contract.
func NamespaceAuthSecret(master []byte, namespace string) *corev1.Secret {
	if len(master) == 0 {
		return nil
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      keysSecretName,
			Namespace: namespace,
			Labels:    map[string]string{managedByLabelKey: managedByValue},
		},
		Data: map[string][]byte{
			fetcherKeyField: hmac.DeriveServiceKeyNS(master, hmac.ServiceFetcher, namespace),
			builderKeyField: hmac.DeriveServiceKeyNS(master, hmac.ServiceBuilder, namespace),
			storageKeyField: hmac.DeriveServiceKeyNS(master, hmac.ServiceStoragesvc, namespace),
		},
	}
}

// EnsureNamespaceAuthSecret writes the per-namespace derived-key Secret into a
// tenant namespace (create-if-absent). An empty master (internalAuth disabled)
// is a no-op. AlreadyExists is ignored so a chart-managed master secret in a
// Helm-configured namespace is left untouched — the controller only mints the
// derived-key secret where one does not yet exist (runtime-onboarded namespaces).
func EnsureNamespaceAuthSecret(ctx context.Context, c client.Client, master []byte, namespace string) error {
	secret := NamespaceAuthSecret(master, namespace)
	if secret == nil {
		return nil
	}
	if err := c.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("provisioning auth secret in %s: %w", namespace, err)
	}
	return nil
}
