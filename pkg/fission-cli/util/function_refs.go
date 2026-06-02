// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/console"
)

// ResolveSecretReferences builds SecretReferences for the given secret names,
// each pointing at namespace. When check is true it verifies every secret exists
// in namespace, warning when one is missing; when strict is also true a
// non-NotFound lookup error aborts with an error (the create path) instead of
// being ignored (the update path). Returns nil for an empty name list.
func ResolveSecretReferences(ctx context.Context, kc kubernetes.Interface, names []string, namespace string, check, strict bool) ([]fv1.SecretReference, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if check {
		for _, name := range names {
			err := SecretExists(ctx, &metav1.ObjectMeta{Namespace: namespace, Name: name}, kc)
			switch {
			case err == nil:
			case k8serrors.IsNotFound(err):
				console.Warn(fmt.Sprintf("Secret %s not found in Namespace: %s. Secret needs to be present in the same namespace as function", name, namespace))
			case strict:
				return nil, fmt.Errorf("error checking secret %s: %w", name, err)
			}
		}
	}
	refs := make([]fv1.SecretReference, 0, len(names))
	for _, name := range names {
		refs = append(refs, fv1.SecretReference{Name: name, Namespace: namespace})
	}
	return refs, nil
}

// ResolveConfigMapReferences mirrors ResolveSecretReferences for config maps.
func ResolveConfigMapReferences(ctx context.Context, kc kubernetes.Interface, names []string, namespace string, check, strict bool) ([]fv1.ConfigMapReference, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if check {
		for _, name := range names {
			err := ConfigMapExists(ctx, &metav1.ObjectMeta{Namespace: namespace, Name: name}, kc)
			switch {
			case err == nil:
			case k8serrors.IsNotFound(err):
				console.Warn(fmt.Sprintf("ConfigMap %s not found in Namespace: %s. ConfigMap needs to be present in the same namespace as function", name, namespace))
			case strict:
				return nil, fmt.Errorf("error checking configmap %s: %w", name, err)
			}
		}
	}
	refs := make([]fv1.ConfigMapReference, 0, len(names))
	for _, name := range names {
		refs = append(refs, fv1.ConfigMapReference{Name: name, Namespace: namespace})
	}
	return refs, nil
}
