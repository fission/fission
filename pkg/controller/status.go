// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fission/fission/pkg/conditions"
)

// ConditionedObject is a CRD object whose status carries a Conditions slice.
// The generated Fission types satisfy it via the GetConditions accessors in
// pkg/apis/core/v1 (status_accessors.go).
type ConditionedObject interface {
	client.Object
	GetConditions() *[]metav1.Condition
}

// SetConditions upserts want onto obj's status conditions and persists them via
// the controller-runtime status writer, collapsing the per-controller status
// dance into one call.
//
// obj is normally the object the Reconciler already fetched through the
// Manager's cache (mgr.GetClient().Get), so no extra read is needed: the
// conditions are mutated in place and written with client.Status().Update,
// which targets the /status subresource directly on the API server. The write
// is best-effort — status never gates reconcile success, so a failure (e.g. a
// conflict from a stale cached object) is logged at V(1) and the next reconcile
// reconverges. ObservedGeneration is stamped from obj, so callers leave it zero.
//
// When nothing would change (meta.SetStatusCondition reports no diff for every
// want), the Update is skipped — important on hot paths where the same
// condition is reasserted repeatedly.
func SetConditions(ctx context.Context, log logr.Logger, c client.Client, obj ConditionedObject, want ...metav1.Condition) {
	if len(want) == 0 {
		return
	}
	gen := obj.GetGeneration()
	changed := false
	for i := range want {
		want[i].ObservedGeneration = gen
		if conditions.Set(obj.GetConditions(), want[i]) {
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := c.Status().Update(ctx, obj); err != nil {
		log.V(1).Info("status update failed", "name", obj.GetName(), "namespace", obj.GetNamespace(), "error", err)
	}
}
