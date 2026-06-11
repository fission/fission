// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"strings"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
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
