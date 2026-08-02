// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"
	"unicode"

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
// Why it takes a namespace SET rather than just the release namespace: under
// static tenancy the internal-auth master is replicated into defaultNamespace
// and every additionalFissionNamespaces, because kubelet cannot resolve a
// cross-namespace secretKeyRef. Those copies prune on the same upgrade, and
// they fail differently from the control-plane one — the fetcher mounts them
// with optional: true, so function pods come up UNSIGNED and every storagesvc
// call 401s, with no CreateContainerConfigError to point at the cause.
//
// Idempotent by construction: already-annotated and absent Secrets are both
// no-ops, so this runs harmlessly on every upgrade forever.
func AdoptSecretsForKeep(ctx context.Context, client kubernetes.Interface, logger logr.Logger, namespaces, names []string) error {
	for _, namespace := range namespaces {
		if namespace == "" {
			continue
		}
		if err := adoptInNamespace(ctx, client, logger, namespace, names); err != nil {
			return err
		}
	}
	return nil
}

// adoptInNamespace stamps every named Secret in one namespace.
func adoptInNamespace(ctx context.Context, client kubernetes.Interface, logger logr.Logger, namespace string, names []string) error {
	for _, name := range names {
		if name == "" {
			continue
		}
		// Patch unconditionally rather than reading first. The merge patch is
		// idempotent, so a re-annotation is a no-op that does not even bump
		// resourceVersion — and skipping the read means this identity needs NO
		// get verb at all. That matters: these are the highest-value Secrets in
		// the namespace (the HMAC master, the JWT signing key, the webhook TLS
		// private key), and a grant that cannot read them is worth far more
		// than one that can.
		//
		// A merge patch on the single annotation, rather than a read-modify-
		// write Update, also means it cannot clobber a concurrent writer's
		// changes to the secret's data — which for the HMAC master would be
		// catastrophic.
		patch := []byte(`{"metadata":{"annotations":{"` + keepPolicyAnnotation + `":"keep"}}}`)
		if _, err := client.CoreV1().Secrets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			if k8serrors.IsNotFound(err) {
				// A fresh install, or a namespace that never had this Secret.
				logger.Info("no live secret to adopt", "secret", name, "namespace", namespace)
				continue
			}
			return fmt.Errorf("annotate secret %s/%s for retention: %w", namespace, name, err)
		}
		logger.Info("annotated secret for retention across upgrades", "secret", name, "namespace", namespace)
	}
	return nil
}

// adoptSecretNames parses the ADOPT_SECRETS env value, accepting either commas
// or whitespace as separators. Accepting both is deliberate: the chart renders
// this list (today via `join " "`) and nothing cross-checks the two ends, so a
// separator mismatch would silently yield one bogus name like "a b" — which
// RBAC rejects, failing every install. Empty entries are ignored so an empty
// list is a clean no-op.
func adoptSecretNames(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}
