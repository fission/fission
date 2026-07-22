// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"strings"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

// makeValidFunction returns a Function object that satisfies v1.Function.Validate()
// so the cross-namespace branches are the only thing under test. The caller may
// override the Environment / PackageRef namespaces to exercise the rejects.
func makeValidFunction(fnNs, envNs, pkgNs string) *v1.Function {
	return &v1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fn-1",
			Namespace: fnNs,
		},
		Spec: v1.FunctionSpec{
			Environment: v1.EnvironmentReference{
				Name:      "env-1",
				Namespace: envNs,
			},
			Package: v1.FunctionPackageRef{
				PackageRef: v1.PackageRef{
					Name:      "pkg-1",
					Namespace: pkgNs,
				},
			},
			InvokeStrategy: v1.InvokeStrategy{
				StrategyType: v1.StrategyTypeExecution,
				ExecutionStrategy: v1.ExecutionStrategy{
					ExecutorType: v1.ExecutorTypePoolmgr,
				},
			},
		},
	}
}

func TestFunctionWebhook_Validate_CrossNamespaceEnvironment(t *testing.T) {
	cases := []struct {
		name         string
		fnNs         string
		envNs        string
		wantRejected bool
	}{
		{name: "empty env.namespace is accepted", fnNs: "default", envNs: "", wantRejected: false},
		{name: "same namespace is accepted", fnNs: "default", envNs: "default", wantRejected: false},
		{name: "cross namespace is rejected", fnNs: "ns-attacker", envNs: "ns-victim", wantRejected: true},
		{name: "cross namespace rejected even when fn in kube-system", fnNs: "kube-system", envNs: "default", wantRejected: true},
	}

	r := &Function{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := r.Validate(makeValidFunction(tc.fnNs, tc.envNs, tc.fnNs))
			if tc.wantRejected {
				if err == nil {
					t.Fatalf("expected rejection, got nil")
				}
				if !strings.Contains(err.Error(), "Environment reference") {
					t.Fatalf("error should reference cross-namespace Environment, got: %v", err)
				}
				if !strings.Contains(err.Error(), tc.envNs) || !strings.Contains(err.Error(), tc.fnNs) {
					t.Fatalf("error should mention both namespaces (%q and %q), got: %v", tc.fnNs, tc.envNs, err)
				}
			} else if err != nil {
				t.Fatalf("expected acceptance, got: %v", err)
			}
		})
	}
}

func TestFunctionWebhook_Validate_CrossNamespacePackage(t *testing.T) {
	cases := []struct {
		name         string
		fnNs         string
		pkgNs        string
		wantRejected bool
	}{
		{name: "empty pkg.namespace is accepted", fnNs: "default", pkgNs: "", wantRejected: false},
		{name: "same namespace is accepted", fnNs: "default", pkgNs: "default", wantRejected: false},
		{name: "cross namespace is rejected", fnNs: "ns-attacker", pkgNs: "ns-victim", wantRejected: true},
	}

	r := &Function{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Keep env.Namespace aligned with fn.Namespace so only the
			// package-ref branch can trigger the cross-ns reject.
			err := r.Validate(makeValidFunction(tc.fnNs, tc.fnNs, tc.pkgNs))
			if tc.wantRejected {
				if err == nil {
					t.Fatalf("expected rejection, got nil")
				}
				if !strings.Contains(err.Error(), "Package reference") {
					t.Fatalf("error should reference cross-namespace Package, got: %v", err)
				}
				if !strings.Contains(err.Error(), tc.pkgNs) || !strings.Contains(err.Error(), tc.fnNs) {
					t.Fatalf("error should mention both namespaces (%q and %q), got: %v", tc.fnNs, tc.pkgNs, err)
				}
			} else if err != nil {
				t.Fatalf("expected acceptance, got: %v", err)
			}
		})
	}
}

// TestFunctionWebhook_Validate_RejectsDangerousPodSpec exercises the
// container-executor PodSpec safety check. Closes GHSA-v455-mv2v-5g92.
func TestFunctionWebhook_Validate_RejectsDangerousPodSpec(t *testing.T) {
	on := true
	cases := []struct {
		name      string
		ps        *apiv1.PodSpec
		wantInErr string
	}{
		{
			name:      "hostNetwork",
			ps:        &apiv1.PodSpec{HostNetwork: true},
			wantInErr: "hostNetwork",
		},
		{
			name: "hostPath volume",
			ps: &apiv1.PodSpec{
				Volumes: []apiv1.Volume{{
					Name: "host-root",
					VolumeSource: apiv1.VolumeSource{
						HostPath: &apiv1.HostPathVolumeSource{Path: "/"},
					},
				}},
			},
			wantInErr: "hostPath",
		},
		{
			name: "privileged container",
			ps: &apiv1.PodSpec{
				Containers: []apiv1.Container{{
					Name:            "user",
					SecurityContext: &apiv1.SecurityContext{Privileged: &on},
				}},
			},
			wantInErr: "privileged",
		},
		{
			name:      "serviceAccountName override",
			ps:        &apiv1.PodSpec{ServiceAccountName: "cluster-admin"},
			wantInErr: "serviceAccountName",
		},
	}

	r := &Function{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn := makeValidFunction("default", "default", "default")
			fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = v1.ExecutorTypeContainer
			fn.Spec.PodSpec = tc.ps
			err := r.Validate(fn)
			if err == nil {
				t.Fatalf("expected rejection for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error must mention %q, got: %v", tc.wantInErr, err)
			}
		})
	}
}

// TestFunctionConcurrencyEnforcementWarning: the annotation fails open (any
// non-"strict" value means router-local accounting), so a typo must earn an
// admission warning while the user is still looking.
func TestFunctionConcurrencyEnforcementWarning(t *testing.T) {
	t.Parallel()
	w := &Function{}
	w.Warner = w

	fn := func(annotations map[string]string) *v1.Function {
		f := &v1.Function{}
		f.Annotations = annotations
		return f
	}

	if got := w.Warnings(fn(nil)); len(got) != 0 {
		t.Fatalf("no annotation must warn nothing, got %v", got)
	}
	if got := w.Warnings(fn(map[string]string{v1.ConcurrencyEnforcementAnnotation: v1.ConcurrencyEnforcementStrict})); len(got) != 0 {
		t.Fatalf("the recognized value must warn nothing, got %v", got)
	}
	got := w.Warnings(fn(map[string]string{v1.ConcurrencyEnforcementAnnotation: "Strict"}))
	if len(got) != 1 {
		t.Fatalf("a typo'd value must warn exactly once, got %v", got)
	}
	if !strings.Contains(got[0], `"Strict"`) || !strings.Contains(got[0], "router-local") {
		t.Fatalf("warning must name the bad value and the consequence, got %q", got[0])
	}
}

// TestWarningsSurfaceThroughValidate pins the GenericWebhook plumbing: the
// warning must reach the admission response through BOTH ValidateCreate and
// ValidateUpdate (the realistic flow for the concurrency-enforcement warning
// is `kubectl annotate` on an existing Function = update), and warnings must
// surface even for a webhook with a Warner but no Validator.
func TestWarningsSurfaceThroughValidate(t *testing.T) {
	t.Parallel()
	fnWith := func(val string) *v1.Function {
		f := &v1.Function{}
		f.Annotations = map[string]string{v1.ConcurrencyEnforcementAnnotation: val}
		return f
	}

	t.Run("with validator", func(t *testing.T) {
		t.Parallel()
		w := &Function{}
		w.Validator = w
		w.Warner = w

		warnings, err := w.ValidateCreate(t.Context(), fnWith("Strict"))
		if err != nil {
			t.Fatalf("create must be admitted (warning, not rejection): %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("ValidateCreate must surface the warning, got %v", warnings)
		}

		warnings, err = w.ValidateUpdate(t.Context(), nil, fnWith("STRICT"))
		if err != nil {
			t.Fatalf("update must be admitted: %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("ValidateUpdate must surface the warning, got %v", warnings)
		}
	})

	t.Run("warner without validator", func(t *testing.T) {
		t.Parallel()
		w := &Function{}
		w.Warner = w // no Validator: warn-only webhooks must still surface warnings

		warnings, err := w.ValidateUpdate(t.Context(), nil, fnWith("Strict"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("warnings must not be gated on Validator, got %v", warnings)
		}
	})
}

func TestFunctionWebhook_Validate_StateOnInfiniteEnv(t *testing.T) {
	t.Parallel()

	envInfinite := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-1", Namespace: "default"},
		Spec:       v1.EnvironmentSpec{Version: 2, AllowedFunctionsPerContainer: v1.AllowedFunctionsPerContainerInfinite},
	}
	envSingle := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-1", Namespace: "default"},
		Spec:       v1.EnvironmentSpec{Version: 2, AllowedFunctionsPerContainer: v1.AllowedFunctionsPerContainerSingle},
	}
	stateFn := func() *v1.Function {
		fn := makeValidFunction("default", "default", "default")
		fn.Spec.State = &v1.StateConfig{}
		return fn
	}

	tests := []struct {
		name    string
		env     *v1.Environment // nil => reader returns NotFound
		reader  bool            // false => nil reader (fail-open)
		wantErr bool
	}{
		{"infinite env rejected", envInfinite, true, true},
		{"single env allowed", envSingle, true, false},
		{"env not found: fail open", nil, true, false},
		{"no reader: fail open", nil, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := fake.NewClientBuilder().WithScheme(scheme.Scheme)
			if tc.env != nil {
				b = b.WithObjects(tc.env)
			}
			r := &Function{}
			if tc.reader {
				r.reader = b.Build()
			}
			err := r.Validate(stateFn())
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "infinite") {
					t.Fatalf("expected infinite-env rejection, got: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
