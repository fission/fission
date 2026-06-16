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

	"github.com/fission/fission/pkg/auth/hmac"
)

const (
	// internalAuthSecretName is the Secret the fetcher/builder containers mount
	// for internal-auth signing. The chart materializes a master-bearing copy
	// today; the tenant controller writes a derived-key-only copy for namespaces
	// it manages (see NamespaceAuthSecret).
	internalAuthSecretName = "fission-internal-auth"

	// Per-namespace derived-key fields. Distinct from the chart's "secret"
	// (master) field so the two can coexist during the migration window before
	// the master is dropped from tenant namespaces.
	fetcherKeyField = "fetcherKey"
	builderKeyField = "builderKey"
	storageKeyField = "storageKey"
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
			Name:      internalAuthSecretName,
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
