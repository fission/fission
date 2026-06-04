// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"bytes"
	"context"
	"maps"
	"os"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// InternalAuthSecretName is the Secret that holds the shared HMAC secret used to
// sign Fission's internal-channel requests (storagesvc /v1/archive, fetcher,
// builder, executor, router). The fetcher sidecar reads FISSION_INTERNAL_AUTH_SECRET
// from it via a secretKeyRef. See docs/internal-auth/00-design.md.
const InternalAuthSecretName = "fission-internal-auth"

// EnsureInternalAuthSecret makes the fission-internal-auth Secret present in
// namespace, mirroring the HMAC values from the caller's own
// FISSION_INTERNAL_AUTH_SECRET[_OLD] env.
//
// Required under watch-all-namespaces: the chart only creates this Secret in the
// configured namespaces, so a function or builder pod in a dynamically-discovered
// namespace would otherwise get an EMPTY FISSION_INTERNAL_AUTH_SECRET (the
// sidecar's secretKeyRef is Optional and the Secret is absent), sign nothing, and
// fail every storagesvc request with HTTP 401. Idempotent and harmless in the
// static-namespace case (the values match), so callers invoke it unconditionally
// alongside EnsureFetcherSA / EnsureBuilderSA.
//
// Reconciles to match internal auth state, so toggling it never leaves the tenant
// copy stale: when disabled (both env values empty) it DELETES the Secret if
// present — a leftover copy would make the function-pod fetcher sidecar (whose
// secretKeyRef is always mounted) keep enforcing HMAC while the control plane no
// longer signs, producing 401s on specialization. When enabled it creates the
// Secret if missing, updates it if the HMAC values drifted, and leaves it
// untouched otherwise. No-op for an empty namespace.
func EnsureInternalAuthSecret(ctx context.Context, kubernetesClient kubernetes.Interface, logger logr.Logger, namespace string) {
	if namespace == "" {
		return
	}
	secret := os.Getenv("FISSION_INTERNAL_AUTH_SECRET")
	oldSecret := os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD")
	disabled := secret == "" && oldSecret == ""

	secrets := kubernetesClient.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, InternalAuthSecretName, metav1.GetOptions{})

	if disabled {
		// Internal auth is off: ensure no tenant copy lingers (so fetchers don't
		// enforce). Only delete when one actually exists, to avoid a delete call
		// on every reconcile in the common (default-off) case.
		if err == nil {
			if derr := secrets.Delete(ctx, InternalAuthSecretName, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				logger.Error(derr, "error deleting stale internal-auth secret", "namespace", namespace)
			}
		}
		return
	}

	data := map[string][]byte{}
	if secret != "" {
		data["secret"] = []byte(secret)
	}
	if oldSecret != "" {
		data["oldSecret"] = []byte(oldSecret)
	}

	if apierrors.IsNotFound(err) {
		_, cerr := secrets.Create(ctx, &apiv1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      InternalAuthSecretName,
				Namespace: namespace,
				Labels:    map[string]string{"application": "fission-internal-auth"},
			},
			Type: apiv1.SecretTypeOpaque,
			Data: data,
		}, metav1.CreateOptions{})
		if cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			logger.Error(cerr, "error creating internal-auth secret", "namespace", namespace)
		}
		return
	}
	if err != nil {
		logger.Error(err, "error getting internal-auth secret", "namespace", namespace)
		return
	}
	if maps.EqualFunc(existing.Data, data, bytes.Equal) {
		return
	}
	existing.Data = data
	if _, uerr := secrets.Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
		logger.Error(uerr, "error updating internal-auth secret", "namespace", namespace)
	}
}
