// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	kauth "github.com/google/go-containerregistry/pkg/authn/kubernetes"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// NoServiceAccount skips the ServiceAccount imagePullSecrets lookup in
// Keychain — for callers whose pod identity has no RBAC on (or no use for)
// a ServiceAccount's pull secrets, e.g. the PUSH path in the builder pod
// (RFC-0012), which authenticates solely via the explicit push secret.
const NoServiceAccount = kauth.NoServiceAccount

// Keychain builds the registry-credential chain for pulling a package image:
// the service account's imagePullSecrets plus the explicit pullSecrets (both
// in namespace ns), falling back to the process-default keychain (and thus
// anonymous). Uses go-containerregistry's pkg/authn/kubernetes resolver — NOT
// k8schain, which would drag the AWS/GCP/Azure credential helpers into the
// static fetcher image.
func Keychain(ctx context.Context, client kubernetes.Interface, ns, serviceAccount string, pullSecrets []apiv1.LocalObjectReference) (authn.Keychain, error) {
	names := make([]string, 0, len(pullSecrets))
	for _, s := range pullSecrets {
		names = append(names, s.Name)
	}
	kc, err := kauth.New(ctx, client, kauth.Options{
		Namespace:          ns,
		ServiceAccountName: serviceAccount,
		ImagePullSecrets:   names,
	})
	if err != nil {
		return nil, fmt.Errorf("building registry keychain for namespace %q: %w", ns, err)
	}
	return authn.NewMultiKeychain(kc, authn.DefaultKeychain), nil
}
