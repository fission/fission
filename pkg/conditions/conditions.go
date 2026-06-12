// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package conditions is a thin, internal seam over
// k8s.io/apimachinery/pkg/api/meta's standard condition helpers, intended for
// Fission controllers that write Status.Conditions on CRDs.
//
// We wrap (rather than re-export) so we can: (a) give controllers a single
// import path to grep for, (b) attach Fission-specific helpers in the
// future (e.g. NewReady, NewProgressing) without breaking call sites, and
// (c) keep one place where we document the LastTransitionTime invariant.
//
// All semantics — including "LastTransitionTime updates iff Status changes" —
// come directly from apimachinery's meta.SetStatusCondition.
package conditions

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Set upserts c into conds by Type. Behaviour mirrors
// meta.SetStatusCondition: LastTransitionTime is set when Status changes
// (or when the condition is new) and preserved on a no-status-change
// update. Reason, Message, and ObservedGeneration always overwrite.
// Returns true iff the slice content meaningfully changed.
func Set(conds *[]metav1.Condition, c metav1.Condition) bool {
	return meta.SetStatusCondition(conds, c)
}

// Find returns a pointer to the condition with the given Type, or nil.
// The returned pointer references storage inside conds; callers that
// intend to mutate should copy first.
// Delete removes the condition of the given type, reporting whether it was
// present. Use when a condition's subject no longer applies (e.g. a stale
// producer-outcome condition after a build that did not involve the producer).
func Delete(conds *[]metav1.Condition, conditionType string) bool {
	return meta.RemoveStatusCondition(conds, conditionType)
}

func Find(conds []metav1.Condition, conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(conds, conditionType)
}

// IsTrue reports whether conds contains a condition of the given Type with
// Status == True. A missing condition or any other Status returns false.
func IsTrue(conds []metav1.Condition, conditionType string) bool {
	return meta.IsStatusConditionTrue(conds, conditionType)
}

// IsAt reports whether conds already contains a condition matching want's
// Type / Status / Reason / ObservedGeneration. Controllers use this to
// fast-path skip a Get + UpdateStatus when nothing meaningful would change
// — important on hot informer paths (e.g., router debounced resync,
// envwatcher AddUpdate) where the same condition is reasserted many times.
// Message is intentionally ignored: it's free-form and shouldn't trigger
// a write if every other field matches.
func IsAt(conds []metav1.Condition, want metav1.Condition) bool {
	existing := Find(conds, want.Type)
	if existing == nil {
		return false
	}
	return existing.Status == want.Status &&
		existing.Reason == want.Reason &&
		existing.ObservedGeneration == want.ObservedGeneration
}

// MessageMaxLen is the upper bound on metav1.Condition.Message enforced
// by the apiserver via the generated CRD schema (maxLength=32768 — see
// e.g. crds/v1/fission.io_packages.yaml). Any longer message would cause
// the entire UpdateStatus to be rejected. Leave a small headroom for any
// future copy/edit churn.
const MessageMaxLen = 32 * 1024

// TruncateMessage trims s to MessageMaxLen, appending an elision marker
// when truncation occurred so the consumer knows to fetch the full text
// elsewhere (e.g., Package.Status.BuildLog still has the untruncated
// build output). Writers that may put user-supplied or err.Error() text
// into Condition.Message should always wrap with this.
func TruncateMessage(s string) string {
	if len(s) <= MessageMaxLen {
		return s
	}
	const ellipsis = "... [truncated; see full text on parent resource]"
	keep := max(MessageMaxLen-len(ellipsis), 0)
	return s[:keep] + ellipsis
}
