// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
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
	// matches what the chart's randAlphaNum 32 produced. Note the signer
	// itself enforces no minimum — it only rejects a zero-length master
	// (pkg/auth/hmac/keys.go) — so this length is the guarantee, not a floor
	// something downstream would catch.
	authMasterBytes = 32

	// provisionAttempts bounds the adopt-the-winner retry; a third round means
	// two runs each won a different namespace, which no value can reconcile.
	provisionAttempts = 3
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

	// Retry, because a create that loses to a concurrent hook Job does not mean
	// this run's master is the right one — it means someone else's is. Re-read
	// and adopt the winner rather than carrying our own value into the
	// remaining namespaces, which is how the set would diverge permanently.
	for attempt := range provisionAttempts {
		// Reuse an existing value if there is one. Reading the master is what
		// the `get` grant on this single name is for: without it a second run
		// could not tell "already provisioned" from "absent" and would mint a
		// new master, which is strictly worse than the read.
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

		lost, err := writeMaster(ctx, client, logger, namespaces, secretName, master)
		if err != nil {
			return err
		}
		if !lost {
			return nil
		}
		logger.Info("lost a create to a concurrent run; re-reading the winning master",
			"secret", secretName, "attempt", attempt+1)
	}
	// Two runs each won a different namespace. There is no value that makes the
	// set consistent, and guessing would leave half the derived keys wrong with
	// no error anywhere, so stop and say so.
	return fmt.Errorf("internal-auth master %q still disagrees across namespaces after %d attempts; "+
		"delete the inconsistent copies and re-run", secretName, provisionAttempts)
}

// writeMaster provisions master into every namespace, reporting whether any
// namespace already held a DIFFERENT one (in which case the caller must adopt
// the winner and try again).
func writeMaster(ctx context.Context, client kubernetes.Interface, logger logr.Logger, namespaces []string, secretName string, master []byte) (bool, error) {
	for _, ns := range namespaces {
		if ns == "" {
			continue
		}
		created, agreed, err := createIfAbsent(ctx, client, ns, secretName, master)
		if err != nil {
			return false, err
		}
		if !agreed {
			return true, nil
		}
		if created {
			logger.Info("created internal-auth master", "secret", secretName, "namespace", ns)
		}
	}
	return false, nil
}

// existingMaster returns the first master value already present in the set.
// Namespaces are checked in order, so the release namespace (first) wins if
// copies ever disagree.
//
// A Secret that exists but carries no usable key is rejected HERE, before the
// caller writes anything. That placement is the point: this scan holds `get` on
// every namespace and runs entirely ahead of the first create, so refusing here
// leaves the cluster untouched. Refusing later — at the create that trips over
// it — would already have written a freshly minted master into the namespaces
// ahead of it, and this identity has neither `patch` nor `delete` on the
// master, so it could never take that back. The operator would then be holding
// one namespace it must populate by hand and another the hook now insists is
// authoritative, which is a worse place to stand than where they started.
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
		v, ok := s.Data[authSecretKey]
		if !ok || len(v) == 0 {
			return nil, false, fmt.Errorf("%s/%s exists but carries no %q value; "+
				"populate the key, delete the Secret, or point internalAuth.existingSecret "+
				"at the Secret you manage, then re-run", ns, secretName, authSecretKey)
		}
		return v, true, nil
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
	return []byte(base64.RawStdEncoding.EncodeToString(raw)), nil
}

// createIfAbsent creates the Secret, reporting whether it created one and
// whether the live object agrees with master.
//
// An AlreadyExists is not success on its own. The Secret that beat us may hold
// a different master (a concurrent hook Job that minted its own) or no usable
// key at all (an aborted rotation, or an ExternalSecret placeholder that
// existingMaster deliberately skipped). Returning "fine" for either is what
// lets the namespaces diverge, and the symptom is 401s from pods signing with
// a key the control plane does not verify — with nothing naming the cause.
func createIfAbsent(ctx context.Context, client kubernetes.Interface, namespace, name string, master []byte) (created, agreed bool, err error) {
	_, err = client.CoreV1().Secrets(namespace).Create(ctx, &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				// NOT sufficient to make this Helm-adoptable, and deliberately
				// not claiming to be. Helm's ownership check wants this label
				// AND the meta.helm.sh/release-{name,namespace} annotations;
				// the hook does not know the release, so it cannot set them.
				// The label is here for selectors and parity with the chart's
				// other objects only. Consequence: switching an autoGenerate
				// install to a templated internalAuth.secret makes Helm refuse
				// to adopt this Secret ("invalid ownership metadata") — see
				// values.yaml, which documents deleting it first.
				"app.kubernetes.io/managed-by": "Helm",
				"application":                  "fission-internal-auth",
			},
		},
		Type: apiv1.SecretTypeOpaque,
		Data: map[string][]byte{authSecretKey: master},
	}, metav1.CreateOptions{})
	switch {
	case err == nil:
		return true, true, nil
	case k8serrors.IsAlreadyExists(err):
		live, getErr := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return false, false, fmt.Errorf("read existing %s/%s: %w", namespace, name, getErr)
		}
		switch v := live.Data[authSecretKey]; {
		case len(v) == 0:
			// existingMaster already rejects a keyless Secret before any write,
			// so reaching this means one appeared BETWEEN that scan and this
			// create. Rare, and unlike the scan it cannot be atomic — earlier
			// namespaces are already written. Still refuse: provisioning
			// around it would leave this namespace keyless for good while the
			// hook exits 0.
			return false, false, fmt.Errorf("%s/%s was created without a %q value while this run was in flight; "+
				"populate the key or delete the Secret, then re-run", namespace, name, authSecretKey)
		case !bytes.Equal(v, master):
			return false, false, nil
		default:
			return false, true, nil
		}
	default:
		return false, false, fmt.Errorf("create %s/%s: %w", namespace, name, err)
	}
}
