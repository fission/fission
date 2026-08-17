// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// sampleConditions returns a deterministic, non-trivial slice of conditions
// suitable for round-trip assertions. Using fixed timestamps avoids flakes
// when comparing post-marshal/unmarshal values.
func sampleConditions() []metav1.Condition {
	t0 := metav1.NewTime(time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC))
	return []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "AllSubsystemsReady",
			Message:            "package built and endpoints reachable",
			LastTransitionTime: t0,
			ObservedGeneration: 1,
		},
		{
			Type:               "Progressing",
			Status:             metav1.ConditionFalse,
			Reason:             "Stable",
			Message:            "no in-flight reconcile",
			LastTransitionTime: t0,
			ObservedGeneration: 1,
		},
	}
}

// roundTripJSON marshals got, unmarshals into a fresh T and compares the JSON
// projections. Comparing the round-tripped Go value to the original via deep
// equality is brittle because metav1.Time embeds a time.Time whose
// *time.Location pointer differs after unmarshal even though the instant is
// identical; the JSON projection is the contract the apiserver and clients
// actually care about.
func roundTripJSON[T any](t *testing.T, got T) {
	t.Helper()
	first, err := json.Marshal(got)
	require.NoError(t, err)

	var target T
	require.NoError(t, json.Unmarshal(first, &target))

	second, err := json.Marshal(target)
	require.NoError(t, err)
	require.JSONEq(t, string(first), string(second))
}

func TestStatusJSONRoundTrip(t *testing.T) {
	conds := sampleConditions()
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"FunctionStatus", func(t *testing.T) { roundTripJSON(t, FunctionStatus{ObservedGeneration: 7, Conditions: conds}) }},
		{"EnvironmentStatus", func(t *testing.T) { roundTripJSON(t, EnvironmentStatus{ObservedGeneration: 3, Conditions: conds}) }},
		{"HTTPTriggerStatus", func(t *testing.T) { roundTripJSON(t, HTTPTriggerStatus{ObservedGeneration: 1, Conditions: conds}) }},
		{"KubernetesWatchTriggerStatus", func(t *testing.T) {
			roundTripJSON(t, KubernetesWatchTriggerStatus{ObservedGeneration: 2, Conditions: conds})
		}},
		{"TimeTriggerStatus", func(t *testing.T) { roundTripJSON(t, TimeTriggerStatus{ObservedGeneration: 4, Conditions: conds}) }},
		{"MessageQueueTriggerStatus", func(t *testing.T) {
			roundTripJSON(t, MessageQueueTriggerStatus{ObservedGeneration: 5, Conditions: conds})
		}},
		{"PackageStatus", func(t *testing.T) {
			roundTripJSON(t, PackageStatus{
				BuildStatus:         BuildStatusSucceeded,
				BuildLog:            "ok",
				LastUpdateTimestamp: metav1.NewTime(time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC)),
				Conditions:          conds,
			})
		}},
		{"CanaryConfigStatus", func(t *testing.T) { roundTripJSON(t, CanaryConfigStatus{Status: "Pending", Conditions: conds}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

// checkDeepCopy asserts dup equals orig and that mutate (which edits dup)
// leaves orig untouched, i.e. the copy has independent backing storage.
func checkDeepCopy[T any](t *testing.T, orig, dup T, mutate func()) {
	t.Helper()
	require.EqualValues(t, orig, dup, "DeepCopy must produce equal value")
	mutate()
	require.NotEqualValues(t, orig, dup, "mutating the copy must not affect the original (independent backing storage)")
}

func TestStatusDeepCopy(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"FunctionStatus", func(t *testing.T) {
			o := FunctionStatus{Conditions: sampleConditions()}
			d := *o.DeepCopy()
			checkDeepCopy(t, o, d, func() { d.Conditions[0].Reason = "Mutated" })
		}},
		{"PackageStatus", func(t *testing.T) {
			o := PackageStatus{Conditions: sampleConditions(), BuildStatus: BuildStatusSucceeded}
			d := *o.DeepCopy()
			checkDeepCopy(t, o, d, func() { d.Conditions[0].Reason = "Mutated" })
		}},
		{"CanaryConfigStatus", func(t *testing.T) {
			o := CanaryConfigStatus{Status: "running", Conditions: sampleConditions()}
			d := *o.DeepCopy()
			checkDeepCopy(t, o, d, func() { d.Conditions[0].Reason = "Mutated" })
		}},
		{"HTTPTriggerStatus", func(t *testing.T) {
			o := HTTPTriggerStatus{Conditions: sampleConditions()}
			d := *o.DeepCopy()
			checkDeepCopy(t, o, d, func() { d.Conditions[0].Reason = "Mutated" })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

// TestPackageStatusBackwardCompat ensures that legacy PackageStatus payloads
// without a `conditions` key unmarshal cleanly with Conditions left as nil.
// Catches accidental `required` constraints or non-omitempty traps on the
// new field.
func TestPackageStatusBackwardCompat(t *testing.T) {
	const legacy = `{"buildstatus":"succeeded","buildlog":"ok","lastUpdateTimestamp":"2026-05-01T12:00:00Z"}`
	var s PackageStatus
	require.NoError(t, json.Unmarshal([]byte(legacy), &s))
	// EqualValues to bridge the typed BuildStatus vs untyped string constant.
	require.EqualValues(t, BuildStatusSucceeded, s.BuildStatus)
	require.Nil(t, s.Conditions)
}

// TestCanaryConfigStatusBackwardCompat is the equivalent check for the older
// `{ "status": "..." }` CanaryConfigStatus shape.
func TestCanaryConfigStatusBackwardCompat(t *testing.T) {
	const legacy = `{"status":"running"}`
	var s CanaryConfigStatus
	require.NoError(t, json.Unmarshal([]byte(legacy), &s))
	require.Equal(t, "running", s.Status)
	require.Nil(t, s.Conditions)
}
