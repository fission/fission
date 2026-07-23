// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"pgregory.net/rapid"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// affectingFields and notAffectingFields are the DECIDED classification from
// docs/rfc/0025-function-versions-aliases-rollback.md (invariant V4). Every
// FunctionSpec field must appear in exactly one of these two sets —
// TestRuntimeAffecting_FieldCoverage enforces that via reflection, so a
// future field addition fails loudly until it is categorized here (and in
// RuntimeAffecting itself).
var affectingFields = map[string]struct{}{
	"Environment":     {},
	"Package":         {},
	"Secrets":         {},
	"ConfigMaps":      {},
	"Resources":       {},
	"InvokeStrategy":  {},
	"Streaming":       {},
	"State":           {},
	"FunctionTimeout": {},
	"Invocation":      {},
	"Concurrency":     {},
	"RequestsPerPod":  {},
	"OnceOnly":        {},
	"PodSpec":         {},
}

var notAffectingFields = map[string]struct{}{
	"IdleTimeout":            {},
	"RetainPods":             {},
	"ProvisionedConcurrency": {},
	"Tool":                   {},
	"Versioning":             {},
}

// TestRuntimeAffecting_FieldCoverage is the completeness guard: it walks
// fv1.FunctionSpec's fields via reflection and asserts each field name
// appears in exactly one of affectingFields / notAffectingFields. Pattern
// follows TestAllTenantContainerSurfacesAreValidated in
// pkg/apis/core/v1/podspec_safety_test.go (walk-types-with-known-set). A new
// FunctionSpec field must be categorized in one (and only one) of the two
// sets above, and wired into RuntimeAffecting if affecting, or it fails this
// test.
func TestRuntimeAffecting_FieldCoverage(t *testing.T) {
	rt := reflect.TypeFor[fv1.FunctionSpec]()
	seen := map[string]struct{}{}
	for i := range rt.NumField() {
		name := rt.Field(i).Name
		seen[name] = struct{}{}

		_, isAffecting := affectingFields[name]
		_, isNotAffecting := notAffectingFields[name]

		switch {
		case isAffecting && isNotAffecting:
			t.Errorf("field %q is listed in BOTH affectingFields and notAffectingFields", name)
		case !isAffecting && !isNotAffecting:
			t.Errorf("field %q on FunctionSpec is not categorized in either affectingFields or "+
				"notAffectingFields; add it to exactly one set (and to RuntimeAffecting's switch if "+
				"affecting) with a one-line rationale comment", name)
		}
	}

	for name := range affectingFields {
		if _, ok := seen[name]; !ok {
			t.Errorf("affectingFields references %q but FunctionSpec no longer has that field; trim it", name)
		}
	}
	for name := range notAffectingFields {
		if _, ok := seen[name]; !ok {
			t.Errorf("notAffectingFields references %q but FunctionSpec no longer has that field; trim it", name)
		}
	}
}

// baseSpec returns a FunctionSpec with a distinct, non-zero value in every
// field so that mutating any single field (affecting or not) produces a
// genuine, observable difference from the base.
func baseSpec() fv1.FunctionSpec {
	idle := 120
	target := 1
	maxAge := metav1.Duration{Duration: 0}
	return fv1.FunctionSpec{
		Environment: fv1.EnvironmentReference{Namespace: "ns", Name: "env1"},
		Package: fv1.FunctionPackageRef{
			PackageRef:   fv1.PackageRef{Namespace: "ns", Name: "pkg1"},
			FunctionName: "handler",
		},
		Secrets:    []fv1.SecretReference{{Namespace: "ns", Name: "secret1"}},
		ConfigMaps: []fv1.ConfigMapReference{{Namespace: "ns", Name: "cm1"}},
		Resources: apiv1.ResourceRequirements{
			Limits: apiv1.ResourceList{apiv1.ResourceCPU: resource.MustParse("100m")},
		},
		InvokeStrategy: fv1.InvokeStrategy{
			ExecutionStrategy: fv1.ExecutionStrategy{
				ExecutorType: fv1.ExecutorType("poolmgr"),
				MinScale:     1,
				MaxScale:     2,
			},
			StrategyType: fv1.StrategyType("execution"),
		},
		FunctionTimeout: 60,
		IdleTimeout:     &idle,
		Streaming: &fv1.StreamingConfig{
			Protocol:           fv1.StreamingProtocol("sse"),
			IdleTimeoutSeconds: 30,
		},
		Tool: &fv1.ToolConfig{Description: "a tool"},
		State: &fv1.StateConfig{
			Keyspace: "ks",
		},
		Invocation: &fv1.InvocationConfig{
			MaxAge: &maxAge,
		},
		Concurrency:    500,
		RequestsPerPod: 1,
		OnceOnly:       false,
		RetainPods:     0,
		ProvisionedConcurrency: &fv1.ProvisionedConcurrencyConfig{
			Target: target,
		},
		Versioning: &fv1.VersioningConfig{Mode: fv1.VersioningMode("auto")},
		PodSpec: &apiv1.PodSpec{
			Containers: []apiv1.Container{{Name: "user", Image: "alpine:3.19"}},
		},
	}
}

// TestRuntimeAffecting_IdenticalSpecsAreNeverAffecting is invariant V4
// itself: RuntimeAffecting(s, s) == false for all s. It is also exercised
// generatively by TestRuntimeAffecting_Rapid_Reflexive below.
func TestRuntimeAffecting_IdenticalSpecsAreNeverAffecting(t *testing.T) {
	s := baseSpec()
	assert.False(t, RuntimeAffecting(s, s), "identical specs must never be classified as runtime-affecting")

	// Deep-copying rather than reusing the same struct value guards against
	// a future accidental pointer-identity shortcut in RuntimeAffecting.
	s2 := *s.DeepCopy()
	assert.False(t, RuntimeAffecting(s, s2))

	var zero fv1.FunctionSpec
	assert.False(t, RuntimeAffecting(zero, zero), "zero-value specs must never be classified as runtime-affecting")
}

// TestRuntimeAffecting_GoldenTable is the field-by-field golden table: for
// every AFFECTING field, a spec pair differing only in that field must
// classify true; for every NOT-AFFECTING field, false.
func TestRuntimeAffecting_GoldenTable(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*fv1.FunctionSpec)
		affects bool
	}{
		{"Environment", func(s *fv1.FunctionSpec) { s.Environment.Name = "env2" }, true},
		{"Package.PackageRef", func(s *fv1.FunctionSpec) { s.Package.PackageRef.Name = "pkg2" }, true},
		{"Package.FunctionName", func(s *fv1.FunctionSpec) { s.Package.FunctionName = "otherHandler" }, true},
		{"Secrets", func(s *fv1.FunctionSpec) {
			s.Secrets = append(s.Secrets, fv1.SecretReference{Namespace: "ns", Name: "secret2"})
		}, true},
		{"ConfigMaps", func(s *fv1.FunctionSpec) {
			s.ConfigMaps = append(s.ConfigMaps, fv1.ConfigMapReference{Namespace: "ns", Name: "cm2"})
		}, true},
		{"Resources", func(s *fv1.FunctionSpec) {
			s.Resources.Limits = apiv1.ResourceList{apiv1.ResourceCPU: resource.MustParse("200m")}
		}, true},
		{"InvokeStrategy", func(s *fv1.FunctionSpec) { s.InvokeStrategy.ExecutionStrategy.MinScale = 5 }, true},
		{"Streaming", func(s *fv1.FunctionSpec) { s.Streaming.IdleTimeoutSeconds = 90 }, true},
		{"Streaming nil->set", func(s *fv1.FunctionSpec) { s.Streaming = nil }, true},
		{"State", func(s *fv1.FunctionSpec) { s.State.Keyspace = "otherks" }, true},
		{"FunctionTimeout", func(s *fv1.FunctionSpec) { s.FunctionTimeout = 90 }, true},
		{"Invocation", func(s *fv1.FunctionSpec) {
			d := metav1.Duration{Duration: 5}
			s.Invocation.MaxAge = &d
		}, true},
		{"Concurrency", func(s *fv1.FunctionSpec) { s.Concurrency = 999 }, true},
		{"RequestsPerPod", func(s *fv1.FunctionSpec) { s.RequestsPerPod = 4 }, true},
		{"OnceOnly", func(s *fv1.FunctionSpec) { s.OnceOnly = true }, true},
		{"PodSpec", func(s *fv1.FunctionSpec) {
			s.PodSpec.Containers[0].Image = "alpine:3.20"
		}, true},
		{"PodSpec nil->set", func(s *fv1.FunctionSpec) { s.PodSpec = nil }, true},

		{"IdleTimeout", func(s *fv1.FunctionSpec) { v := 999; s.IdleTimeout = &v }, false},
		{"RetainPods", func(s *fv1.FunctionSpec) { s.RetainPods = 3 }, false},
		{"ProvisionedConcurrency", func(s *fv1.FunctionSpec) { s.ProvisionedConcurrency.Target = 9 }, false},
		{"ProvisionedConcurrency nil->set", func(s *fv1.FunctionSpec) { s.ProvisionedConcurrency = nil }, false},
		{"Tool", func(s *fv1.FunctionSpec) { s.Tool.Description = "different tool" }, false},
		{"Tool nil->set", func(s *fv1.FunctionSpec) { s.Tool = nil }, false},
		{"Versioning", func(s *fv1.FunctionSpec) { s.Versioning.Mode = fv1.VersioningMode("manual") }, false},
		{"Versioning nil->set", func(s *fv1.FunctionSpec) { s.Versioning = nil }, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := baseSpec()
			mutated := *old.DeepCopy()
			tc.mutate(&mutated)

			require.NotEqual(t, old, mutated, "mutate func for %q produced no change from base — test is not exercising anything", tc.name)

			got := RuntimeAffecting(old, mutated)
			assert.Equal(t, tc.affects, got, "RuntimeAffecting(old, mutated) for field %q", tc.name)

			// RuntimeAffecting must be symmetric in the sense that comparing
			// in the other direction (mutated -> old) gives the same verdict.
			assert.Equal(t, tc.affects, RuntimeAffecting(mutated, old), "RuntimeAffecting(mutated, old) for field %q", tc.name)
		})
	}
}

// TestRuntimeAffecting_Rapid_Reflexive is a property test: for any
// generated spec s, RuntimeAffecting(s, s) is always false. The generator
// is deliberately simple — a handful of representative fields (Environment
// name, FunctionTimeout, Concurrency, OnceOnly, a couple of the not-
// affecting fields) get random values rather than a full arbitrary
// FunctionSpec, since a from-scratch generator for every nested type
// (PodSpec, ResourceRequirements, InvokeStrategy, ...) would mostly be
// re-testing DeepEqual/DeepCopy rather than the classifier's field
// selection, which the golden table above already covers exhaustively.
func TestRuntimeAffecting_Rapid_Reflexive(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := randomSpec(rt)
		assert.False(t, RuntimeAffecting(s, s), "RuntimeAffecting(s, s) must be false for every s")
	})
}

// TestRuntimeAffecting_Rapid_NotAffectingMutationNeverAffects generates a
// random spec, mutates exactly one randomly-chosen NOT-AFFECTING field, and
// asserts the classifier still reports false.
func TestRuntimeAffecting_Rapid_NotAffectingMutationNeverAffects(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := randomSpec(rt)
		mutated := *s.DeepCopy()

		switch rapid.SampledFrom([]string{
			"IdleTimeout", "RetainPods", "ProvisionedConcurrency", "Tool", "Versioning",
		}).Draw(rt, "field") {
		case "IdleTimeout":
			v := rapid.IntRange(0, 100000).Draw(rt, "idleTimeout")
			mutated.IdleTimeout = &v
		case "RetainPods":
			mutated.RetainPods = rapid.IntRange(0, 100).Draw(rt, "retainPods")
		case "ProvisionedConcurrency":
			mutated.ProvisionedConcurrency = &fv1.ProvisionedConcurrencyConfig{
				Target: rapid.IntRange(1, 20).Draw(rt, "target"),
			}
		case "Tool":
			mutated.Tool = &fv1.ToolConfig{
				Description: rapid.StringN(0, 40, -1).Draw(rt, "toolDescription"),
			}
		case "Versioning":
			mode := rapid.SampledFrom([]string{"auto", "manual"}).Draw(rt, "mode")
			mutated.Versioning = &fv1.VersioningConfig{Mode: fv1.VersioningMode(mode)}
		}

		assert.False(t, RuntimeAffecting(s, mutated), "mutating only a NOT-AFFECTING field must never classify as runtime-affecting")
	})
}

// randomSpec draws a FunctionSpec with random values in a handful of
// representative affecting and not-affecting fields, holding the rest at
// baseSpec()'s fixed values. See the doc comment on
// TestRuntimeAffecting_Rapid_Reflexive for why the generator stays this
// narrow.
func randomSpec(rt *rapid.T) fv1.FunctionSpec {
	s := baseSpec()
	s.Environment.Name = rapid.StringMatching(`[a-z][a-z0-9]{0,10}`).Draw(rt, "envName")
	s.FunctionTimeout = rapid.IntRange(1, 600).Draw(rt, "functionTimeout")
	s.Concurrency = rapid.IntRange(1, 1000).Draw(rt, "concurrency")
	s.RequestsPerPod = rapid.IntRange(1, 50).Draw(rt, "requestsPerPod")
	s.OnceOnly = rapid.Bool().Draw(rt, "onceOnly")
	s.RetainPods = rapid.IntRange(0, 50).Draw(rt, "retainPods")
	idle := rapid.IntRange(0, 100000).Draw(rt, "idleTimeout")
	s.IdleTimeout = &idle
	return s
}
