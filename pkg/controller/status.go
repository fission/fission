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

// StatusClient is the subset of a generated typed client used to read and
// persist an object's status. The namespaced Fission clients
// (e.g. fissionClient.CoreV1().TimeTriggers(ns)) satisfy it for T being the
// matching pointer type (*fv1.TimeTrigger, *fv1.Package, ...).
type StatusClient[T ConditionedObject] interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (T, error)
	UpdateStatus(ctx context.Context, obj T, opts metav1.UpdateOptions) (T, error)
}

// SetConditions upserts want onto obj's status conditions and persists them via
// UpdateStatus, collapsing the IsAt fast-path / Get / re-check / Set /
// UpdateStatus dance every Fission controller previously hand-rolled.
//
// It is best-effort: status writes never gate reconcile success, so failures
// are logged at V(1) and swallowed. ObservedGeneration on each want condition
// is stamped from the object being written, so callers leave it zero.
//
// The fast-path uses obj's in-hand conditions to skip the Get entirely when
// nothing would change — important on hot paths where the same condition is
// reasserted repeatedly. After a Get it re-checks against the fresh object so a
// concurrent writer's identical state also short-circuits.
func SetConditions[T ConditionedObject](ctx context.Context, log logr.Logger, sc StatusClient[T], obj T, want ...metav1.Condition) {
	if len(want) == 0 {
		return
	}
	stamp(want, obj.GetGeneration())
	if allAt(*obj.GetConditions(), want) {
		return
	}
	fresh, err := sc.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if err != nil {
		log.V(1).Info("status: get failed", "name", obj.GetName(), "namespace", obj.GetNamespace(), "error", err)
		return
	}
	stamp(want, fresh.GetGeneration())
	if allAt(*fresh.GetConditions(), want) {
		return
	}
	for _, w := range want {
		conditions.Set(fresh.GetConditions(), w)
	}
	if _, err := sc.UpdateStatus(ctx, fresh, metav1.UpdateOptions{}); err != nil {
		log.V(1).Info("status: update failed", "name", obj.GetName(), "namespace", obj.GetNamespace(), "error", err)
	}
}

// stamp re-writes ObservedGeneration on every want condition to gen, so the
// IsAt comparison reflects the generation of the object actually being written.
func stamp(want []metav1.Condition, gen int64) {
	for i := range want {
		want[i].ObservedGeneration = gen
	}
}

// allAt reports whether every want condition is already present in have with a
// matching Type/Status/Reason/ObservedGeneration (Message ignored, per IsAt).
func allAt(have []metav1.Condition, want []metav1.Condition) bool {
	for _, w := range want {
		if !conditions.IsAt(have, w) {
			return false
		}
	}
	return true
}
