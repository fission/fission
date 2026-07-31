// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
// force is passed on first adoption: CRDs applied by the documented
// `kubectl create -k crds/v1` flow carry managedFields owned by kubectl-create
// as an Update operation, so a first apply from a new field manager conflicts
// on precisely the fields a schema-changing upgrade needs to change. Forcing
// takes ownership and reverts hand-edits to a Fission CRD (added printer
// columns, relaxed validation) — documented, with crds.mode=none as the escape
// hatch for installs that need those preserved.
func (client *PreUpgradeTaskClient) ApplyCRDs(ctx context.Context, force bool) error {
	manifests, err := crds.All()
	if err != nil {
		return err
	}
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
		client.logger.Info("applied CRD", "name", applied.Name, "resourceVersion", applied.ResourceVersion)
	}
	client.logger.Info("CRD bundle applied", "count", len(manifests))
	return nil
}

// crdApplyBody validates one embedded manifest and returns its name plus the
// JSON body to apply. Decoding first is what catches a malformed or non-CRD
// document at the hook rather than as an opaque apiserver rejection, and it
// enforces the two invariants the admission policy also asserts: the group is
// a fission.io group, and no conversion webhook is declared.
func crdApplyBody(m crds.Manifest) (string, []byte, error) {
	var crd apiextv1.CustomResourceDefinition
	if err := yaml.Unmarshal(m.YAML, &crd); err != nil {
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
