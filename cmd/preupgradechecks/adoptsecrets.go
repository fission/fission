// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"
)

// keepPolicyAnnotation tells Helm never to delete an object it would otherwise
// consider removed from the release.
const keepPolicyAnnotation = "helm.sh/resource-policy"

// AdoptSecretsForKeep stamps helm.sh/resource-policy=keep onto live Secrets so
// the chart can stop rendering them without Helm pruning them on upgrade.
//
// Why this exists. Several Secrets are generated in the templating layer today
// (the internal-auth HMAC master, the router's password/JWT signing key, the
// webhook serving cert). Moving generation out of templating — which RFC-0029
// §3 requires, because `lookup` returns empty under `helm template` and a
// GitOps sync would otherwise re-mint them on every reconcile — means the
// template stops rendering the object. Helm deletes resources that disappear
// from a release, so the naive change PRUNES the live Secret on the first
// upgrade. For the internal-auth master that wedges every control-plane pod in
// CreateContainerConfigError, because internalAuth.envs mounts it with a
// required secretKeyRef.
//
// Why an annotation on the LIVE object works, verified against a real cluster
// (Helm v4.2.3, kind): Helm reads helm.sh/resource-policy from the object as
// it exists in the cluster at delete time, NOT from the stored release
// manifest. A Secret installed WITHOUT the annotation, then annotated
// out-of-band, survives an upgrade that stops rendering it. Pre-upgrade hooks
// complete before Helm applies manifests and computes the deletion set, so
// stamping here is a valid one-release migration — no intermediate release
// that users must pass through.
//
// Idempotent by construction: already-annotated and absent Secrets are both
// no-ops, so this runs harmlessly on every upgrade forever.
func AdoptSecretsForKeep(ctx context.Context, client kubernetes.Interface, logger logr.Logger, namespace string, names []string) error {
	for _, name := range names {
		if name == "" {
			continue
		}
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				// Nothing to adopt: a fresh install, or an install that never
				// had this Secret. The generator creates it later.
				logger.Info("no live secret to adopt", "secret", name, "namespace", namespace)
				continue
			}
			return fmt.Errorf("read secret %s/%s for adoption: %w", namespace, name, err)
		}
		if secret.Annotations[keepPolicyAnnotation] == "keep" {
			continue
		}

		// A merge patch on the single annotation, rather than a read-modify-
		// write Update: it cannot clobber a concurrent writer's changes to the
		// secret's data, which for the HMAC master would be catastrophic.
		patch := []byte(`{"metadata":{"annotations":{"` + keepPolicyAnnotation + `":"keep"}}}`)
		if _, err := client.CoreV1().Secrets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("annotate secret %s/%s for retention: %w", namespace, name, err)
		}
		logger.Info("annotated secret for retention across upgrades", "secret", name, "namespace", namespace)
	}
	return nil
}

// adoptSecretNames parses the comma-separated ADOPT_SECRETS env value, ignoring
// empty entries so a chart that renders an empty list is a clean no-op.
func adoptSecretNames(raw string) []string {
	var out []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}
