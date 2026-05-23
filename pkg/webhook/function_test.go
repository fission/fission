/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package webhook

import (
	"strings"
	"testing"

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
