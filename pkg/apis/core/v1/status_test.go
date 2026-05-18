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

// statusCase couples a Status value with a freshly-constructed empty value
// for unmarshal targets — needed because *Status types are distinct and we
// want to keep this table generic over them.
type statusCase struct {
	name string
	// got is the populated value to marshal.
	got any
	// fresh returns a zero-valued instance of the same concrete type.
	fresh func() any
}

func statusCases() []statusCase {
	conds := sampleConditions()
	return []statusCase{
		{
			name:  "FunctionStatus",
			got:   FunctionStatus{ObservedGeneration: 7, Conditions: conds},
			fresh: func() any { return &FunctionStatus{} },
		},
		{
			name:  "EnvironmentStatus",
			got:   EnvironmentStatus{ObservedGeneration: 3, Conditions: conds},
			fresh: func() any { return &EnvironmentStatus{} },
		},
		{
			name:  "HTTPTriggerStatus",
			got:   HTTPTriggerStatus{ObservedGeneration: 1, Conditions: conds},
			fresh: func() any { return &HTTPTriggerStatus{} },
		},
		{
			name:  "KubernetesWatchTriggerStatus",
			got:   KubernetesWatchTriggerStatus{ObservedGeneration: 2, Conditions: conds},
			fresh: func() any { return &KubernetesWatchTriggerStatus{} },
		},
		{
			name:  "TimeTriggerStatus",
			got:   TimeTriggerStatus{ObservedGeneration: 4, Conditions: conds},
			fresh: func() any { return &TimeTriggerStatus{} },
		},
		{
			name:  "MessageQueueTriggerStatus",
			got:   MessageQueueTriggerStatus{ObservedGeneration: 5, Conditions: conds},
			fresh: func() any { return &MessageQueueTriggerStatus{} },
		},
		{
			name: "PackageStatus",
			got: PackageStatus{
				BuildStatus:         BuildStatusSucceeded,
				BuildLog:            "ok",
				LastUpdateTimestamp: metav1.NewTime(time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC)),
				Conditions:          conds,
			},
			fresh: func() any { return &PackageStatus{} },
		},
		{
			name:  "CanaryConfigStatus",
			got:   CanaryConfigStatus{Status: "Pending", Conditions: conds},
			fresh: func() any { return &CanaryConfigStatus{} },
		},
	}
}

func TestStatusJSONRoundTrip(t *testing.T) {
	for _, tc := range statusCases() {
		t.Run(tc.name, func(t *testing.T) {
			first, err := json.Marshal(tc.got)
			require.NoError(t, err)

			target := tc.fresh()
			require.NoError(t, json.Unmarshal(first, target))

			// Comparing the round-tripped Go value to the original via deep
			// equality is brittle because metav1.Time embeds a time.Time whose
			// *time.Location pointer differs after unmarshal even though the
			// instant is identical. We compare the JSON projection instead —
			// it's the contract the apiserver and clients actually care about.
			second, err := json.Marshal(derefAny(target))
			require.NoError(t, err)
			require.JSONEq(t, string(first), string(second))
		})
	}
}

func TestStatusDeepCopy(t *testing.T) {
	cases := []struct {
		name        string
		copy        func() (orig, dup any, mutate func())
		expectEqual func(orig, dup any) bool
	}{
		{
			name: "FunctionStatus",
			copy: func() (any, any, func()) {
				o := FunctionStatus{Conditions: sampleConditions()}
				d := *o.DeepCopy()
				return o, d, func() { d.Conditions[0].Reason = "Mutated" }
			},
		},
		{
			name: "PackageStatus",
			copy: func() (any, any, func()) {
				o := PackageStatus{Conditions: sampleConditions(), BuildStatus: BuildStatusSucceeded}
				d := *o.DeepCopy()
				return o, d, func() { d.Conditions[0].Reason = "Mutated" }
			},
		},
		{
			name: "CanaryConfigStatus",
			copy: func() (any, any, func()) {
				o := CanaryConfigStatus{Status: "running", Conditions: sampleConditions()}
				d := *o.DeepCopy()
				return o, d, func() { d.Conditions[0].Reason = "Mutated" }
			},
		},
		{
			name: "HTTPTriggerStatus",
			copy: func() (any, any, func()) {
				o := HTTPTriggerStatus{Conditions: sampleConditions()}
				d := *o.DeepCopy()
				return o, d, func() { d.Conditions[0].Reason = "Mutated" }
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig, dup, mutate := tc.copy()
			require.EqualValues(t, orig, dup, "DeepCopy must produce equal value")
			mutate()
			require.NotEqualValues(t, orig, dup, "mutating the copy must not affect the original (independent backing storage)")
		})
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

// derefAny unwraps the *T returned by statusCase.fresh into a T so the
// table-driven test can EqualValues against the populated input directly.
func derefAny(p any) any {
	switch v := p.(type) {
	case *FunctionStatus:
		return *v
	case *EnvironmentStatus:
		return *v
	case *HTTPTriggerStatus:
		return *v
	case *KubernetesWatchTriggerStatus:
		return *v
	case *TimeTriggerStatus:
		return *v
	case *MessageQueueTriggerStatus:
		return *v
	case *PackageStatus:
		return *v
	case *CanaryConfigStatus:
		return *v
	default:
		return p
	}
}
