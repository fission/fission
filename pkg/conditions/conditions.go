/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
func Find(conds []metav1.Condition, conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(conds, conditionType)
}

// IsTrue reports whether conds contains a condition of the given Type with
// Status == True. A missing condition or any other Status returns false.
func IsTrue(conds []metav1.Condition, conditionType string) bool {
	return meta.IsStatusConditionTrue(conds, conditionType)
}
