// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// internalAuthMasterBytes is the length of the generated HMAC master before
// encoding. 32 bytes matches the HKDF construction's security level; the
// derived per-service and per-namespace keys are all HKDF-expanded from it.
const internalAuthMasterBytes = 32

// GenerateInternalAuthSecret creates the internal-auth Secret with a fresh
// random master, and does nothing if it already exists (RFC-0029 §3).
//
// Create-if-absent is what makes this idempotent under any renderer: a
// templated secret can only preserve its value through Helm's `lookup`, which
// returns empty under `helm template` — the way Argo CD and Flux render — so a
// templated master is re-minted on every sync, rotating every derived key
// underneath running pods across fail-closed channels. An in-cluster action
// has no such failure mode.
//
// The ServiceAccount that runs this has `create` on secrets and nothing else,
// so this code cannot read an existing master even by accident: AlreadyExists
// on create is the only signal it needs, and it never sees the stored value.
func GenerateInternalAuthSecret(ctx context.Context, client kubernetes.Interface, logger logr.Logger, namespace, name string) error {
	master := make([]byte, internalAuthMasterBytes)
	if _, err := rand.Read(master); err != nil {
		return fmt.Errorf("generate internal-auth master: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"application": "fission-internal-auth"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			// base64 of the raw bytes: the value travels through env vars and
			// per-namespace key transport, which are not binary-safe.
			"secret": []byte(base64.StdEncoding.EncodeToString(master)),
		},
	}
	_, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	switch {
	case err == nil:
		logger.Info("generated internal-auth master", "secret", name, "namespace", namespace)
		return nil
	case k8serrors.IsAlreadyExists(err):
		// The expected outcome on every run after the first, and on every
		// repeated GitOps sync. Not an error, and deliberately not a read.
		logger.Info("internal-auth master already present, leaving it alone", "secret", name, "namespace", namespace)
		return nil
	default:
		return fmt.Errorf("create internal-auth secret %s/%s: %w", namespace, name, err)
	}
}

// internalAuthGenParams reads the generator Job's environment.
func internalAuthGenParams() (namespace, name string, err error) {
	namespace = os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		return "", "", fmt.Errorf("POD_NAMESPACE is not set")
	}
	name = os.Getenv("INTERNAL_AUTH_SECRET_NAME")
	if name == "" {
		return "", "", fmt.Errorf("INTERNAL_AUTH_SECRET_NAME is not set")
	}
	return namespace, name, nil
}
