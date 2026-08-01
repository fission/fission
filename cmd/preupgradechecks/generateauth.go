// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// authSecretKey is the data key holding the HMAC master.
	authSecretKey = "secret"
	// authMasterBytes is the generated master's length before base64. 32 bytes
	// matches what the chart's randAlphaNum 32 produced, and exceeds the
	// minimum the signer enforces.
	authMasterBytes = 32
)

// GenerateAuthSecret creates the internal-auth master where it is missing,
// using ONE value across every namespace it writes.
//
// Why this exists (RFC-0029 §10): the chart generated the master in the
// templating layer, preserved across upgrades by `lookup`. Under a GitOps
// renderer `lookup` returns empty — Argo and Flux run `helm template` — so the
// value was re-minted on every sync, breaking every pod signed with the
// previous one. Generation has to be an idempotent in-cluster action instead.
//
// The namespace set is supplied by the caller, NOT derived here, and that is
// deliberate: it is tenancy-dependent, and the chart already computes it in one
// place (fission.adoptSecretNamespaces). Under STATIC tenancy the master is
// replicated into the release namespace plus defaultNamespace plus each
// additionalFissionNamespaces, because kubelet cannot resolve a cross-namespace
// secretKeyRef. Under DYNAMIC or CLUSTER tenancy it goes to the release
// namespace ONLY — tenant namespaces receive controller-owned derived keys and
// the master must never land there, which is the whole isolation end state.
// Duplicating that rule in Go would be a second source of truth for a
// security-relevant decision.
//
// Idempotent by construction, which matters because this runs on every install
// AND every upgrade: an existing master anywhere in the set is reused rather
// than regenerated, and a namespace that already has it is left untouched. A
// regenerated master would rotate every derived key with no dual-accept window.
func GenerateAuthSecret(ctx context.Context, client kubernetes.Interface, logger logr.Logger, namespaces []string, secretName string) error {
	if len(namespaces) == 0 || secretName == "" {
		return nil
	}

	// Reuse an existing value if there is one. Reading the master is what the
	// `get` grant on this single name is for: without it a second run could not
	// tell "already provisioned" from "absent" and would mint a new master,
	// which is strictly worse than the read.
	master, found, err := existingMaster(ctx, client, namespaces, secretName)
	if err != nil {
		return err
	}
	if !found {
		master, err = newMaster()
		if err != nil {
			return err
		}
		logger.Info("generating internal-auth master", "secret", secretName)
	}

	for _, ns := range namespaces {
		if ns == "" {
			continue
		}
		created, err := createIfAbsent(ctx, client, ns, secretName, master)
		if err != nil {
			return err
		}
		if created {
			logger.Info("created internal-auth master", "secret", secretName, "namespace", ns)
		}
	}
	return nil
}

// existingMaster returns the first master value already present in the set.
// Namespaces are checked in order, so the release namespace (first) wins if
// copies ever disagree.
func existingMaster(ctx context.Context, client kubernetes.Interface, namespaces []string, secretName string) ([]byte, bool, error) {
	for _, ns := range namespaces {
		if ns == "" {
			continue
		}
		s, err := client.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			return nil, false, fmt.Errorf("read %s/%s: %w", ns, secretName, err)
		}
		if v, ok := s.Data[authSecretKey]; ok && len(v) > 0 {
			return v, true, nil
		}
	}
	return nil, false, nil
}

func newMaster() ([]byte, error) {
	raw := make([]byte, authMasterBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate internal-auth master: %w", err)
	}
	// base64 so the value is safe in an env var and in `kubectl get -o yaml`
	// round-trips, matching the shape the chart produced.
	out := make([]byte, base64.RawStdEncoding.EncodedLen(len(raw)))
	base64.RawStdEncoding.Encode(out, raw)
	return out, nil
}

// createIfAbsent creates the Secret and reports whether it did. An
// AlreadyExists from a concurrent starter is success, not an error: two hook
// Jobs racing must converge, not fail the install.
func createIfAbsent(ctx context.Context, client kubernetes.Interface, namespace, name string, master []byte) (bool, error) {
	_, err := client.CoreV1().Secrets(namespace).Create(ctx, &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				// Marks the object as Helm-managed so a later
				// internalAuth.secret rotation can adopt it rather than
				// failing on an unowned object.
				"app.kubernetes.io/managed-by": "Helm",
				"application":                  "fission-internal-auth",
			},
		},
		Type: apiv1.SecretTypeOpaque,
		Data: map[string][]byte{authSecretKey: master},
	}, metav1.CreateOptions{})
	switch {
	case err == nil:
		return true, nil
	case k8serrors.IsAlreadyExists(err):
		return false, nil
	default:
		return false, fmt.Errorf("create %s/%s: %w", namespace, name, err)
	}
}
