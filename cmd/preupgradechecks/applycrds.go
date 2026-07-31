// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	"github.com/fission/fission/crds"
)

// crdFieldManager is the server-side-apply field manager the hook owns. It is
// distinct from Helm's and from kubectl's so ownership is attributable, and
// stable across releases so successive upgrades converge instead of fighting.
const crdFieldManager = "fission-crd-hook"

// ApplyCRDs server-side-applies the CRD bundle this binary was built with, so
// the schemas and the controllers that depend on them ship as one artifact
// (RFC-0029 §1).
//
// Server-side apply rather than create-or-update, for three reasons: four of
// the generated CRDs exceed the 262,144-byte client-side-apply annotation
// limit, so client-side apply cannot round-trip them at all; SSA creates and
// updates through one call, which a `pre-install,pre-upgrade` hook needs; and
// it makes field ownership explicit and attributable.
//
// The request body is the generated manifest converted to JSON — byte-for-byte
// the intent in crds/v1, with nothing added. Hand-assembling an apply
// configuration would mean maintaining a parallel copy of the CRD schema
// structure, and any field it failed to carry would be silently dropped from
// the applied schema.
//
// force (crds.forceConflicts, default true) applies to EVERY apply, not only
// the first: it exists because CRDs from the documented
// `kubectl create -k crds/v1` flow carry managedFields owned by kubectl-create
// as an Update operation, so an apply from a new field manager conflicts on
// precisely the fields a schema-changing upgrade must change. The cost of
// leaving it on permanently is that it also silently wins against any other
// writer — a hand-added CEL rule on a Fission CRD, or a GitOps controller
// managing the same objects. Setting crds.forceConflicts=false turns that
// silence into a hard conflict error, which is the right choice once an
// install is past adoption; crds.mode=none opts out of chart-managed CRDs
// entirely.
func (client *PreUpgradeTaskClient) ApplyCRDs(ctx context.Context, force bool) error {
	manifests, err := crds.All()
	if err != nil {
		return err
	}
	applying := make([]string, 0, len(manifests))
	for _, m := range manifests {
		name, jsonBytes, err := crdApplyBody(m)
		if err != nil {
			return err
		}
		applied, err := client.apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Patch(
			ctx, name, types.ApplyPatchType, jsonBytes,
			metav1.PatchOptions{FieldManager: crdFieldManager, Force: &force},
		)
		if err != nil {
			return fmt.Errorf("apply CRD %s: %w", name, err)
		}
		applying = append(applying, applied.Name)
		client.logger.Info("applied CRD", "name", applied.Name, "resourceVersion", applied.ResourceVersion)
	}

	// A CRD is not servable the instant the apply returns: the apiserver has
	// to accept the schema and publish discovery, which takes ~100ms. The very
	// next thing this binary does is LIST Functions, so without this wait a
	// fresh install races its own CRD creation and dies on "the server could
	// not find the requested resource" — with backoffLimit 0, that is a failed
	// install, not a retry.
	if err := client.waitForEstablished(ctx, applying); err != nil {
		return err
	}
	client.logger.Info("CRD bundle applied", "count", len(manifests))
	return nil
}

// establishedTimeout bounds the wait for the apiserver to accept and publish
// the applied schemas.
const establishedTimeout = 2 * time.Minute

// waitForEstablished blocks until every named CRD reports Established=True, or
// the timeout elapses. NamesAccepted=False is terminal (a name collision with
// another CRD) and fails immediately rather than burning the whole budget.
func (client *PreUpgradeTaskClient) waitForEstablished(ctx context.Context, names []string) error {
	ctx, cancel := context.WithTimeout(ctx, establishedTimeout)
	defer cancel()
	for _, name := range names {
		err := wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
			crd, err := client.apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				// The object was just applied; a transient read error is worth
				// retrying inside the budget rather than failing the install.
				return false, nil
			}
			for _, c := range crd.Status.Conditions {
				switch {
				case c.Type == apiextv1.Established && c.Status == apiextv1.ConditionTrue:
					return true, nil
				case c.Type == apiextv1.NamesAccepted && c.Status == apiextv1.ConditionFalse:
					return false, fmt.Errorf("CRD %s has conflicting names: %s", name, c.Message)
				}
			}
			return false, nil
		})
		if err != nil {
			return fmt.Errorf("waiting for CRD %s to be established: %w", name, err)
		}
	}
	return nil
}

// crdApplyBody validates one embedded manifest and returns its name plus the
// JSON body to apply. Decoding first is what catches a malformed or non-CRD
// document at the hook rather than as an opaque apiserver rejection, and it
// enforces the same two invariants the admission policy asserts — group is
// exactly fission.io, and no conversion webhook — so the two agree rather
// than one being looser than the other.
func crdApplyBody(m crds.Manifest) (string, []byte, error) {
	var crd apiextv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(m.YAML, &crd); err != nil {
		return "", nil, fmt.Errorf("parse embedded CRD %s: %w", m.Name, err)
	}
	if crd.Kind != "CustomResourceDefinition" {
		return "", nil, fmt.Errorf("embedded manifest %s is a %q, not a CustomResourceDefinition", m.Name, crd.Kind)
	}
	if crd.Name == "" {
		return "", nil, fmt.Errorf("embedded CRD %s has no metadata.name", m.Name)
	}
	if crd.Spec.Group != "fission.io" {
		return "", nil, fmt.Errorf("embedded CRD %s declares group %q; this hook only manages fission.io CRDs", m.Name, crd.Spec.Group)
	}
	if c := crd.Spec.Conversion; c != nil && c.Strategy != apiextv1.NoneConverter {
		return "", nil, fmt.Errorf("embedded CRD %s declares conversion strategy %q; Fission CRDs use a single storage version and no conversion webhook", m.Name, c.Strategy)
	}
	jsonBytes, err := yaml.YAMLToJSON(m.YAML)
	if err != nil {
		return "", nil, fmt.Errorf("convert embedded CRD %s to JSON: %w", m.Name, err)
	}
	return crd.Name, jsonBytes, nil
}
