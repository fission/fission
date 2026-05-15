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

// makeValidPackage returns a Package object that satisfies v1.Package.Validate()
// so the cross-namespace branch is the only thing under test.
func makeValidPackage(pkgNs, envNs string) *v1.Package {
	return &v1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pkg-1",
			Namespace: pkgNs,
		},
		Spec: v1.PackageSpec{
			Environment: v1.EnvironmentReference{
				Name:      "env-1",
				Namespace: envNs,
			},
		},
		Status: v1.PackageStatus{
			BuildStatus: v1.BuildStatusPending,
		},
	}
}

func TestPackageWebhook_Validate_CrossNamespaceEnvironment(t *testing.T) {
	cases := []struct {
		name         string
		pkgNs        string
		envNs        string
		wantRejected bool
	}{
		{name: "empty env.namespace is accepted", pkgNs: "default", envNs: "", wantRejected: false},
		{name: "same namespace is accepted", pkgNs: "default", envNs: "default", wantRejected: false},
		{name: "cross namespace is rejected", pkgNs: "ns-attacker", envNs: "ns-victim", wantRejected: true},
		{name: "cross namespace rejected even when pkg in kube-system", pkgNs: "kube-system", envNs: "default", wantRejected: true},
	}

	r := &Package{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := r.Validate(makeValidPackage(tc.pkgNs, tc.envNs))
			if tc.wantRejected {
				if err == nil {
					t.Fatalf("expected rejection, got nil")
				}
				if !strings.Contains(err.Error(), "spec.environment.namespace") {
					t.Fatalf("error should reference spec.environment.namespace, got: %v", err)
				}
				if !strings.Contains(err.Error(), tc.envNs) || !strings.Contains(err.Error(), tc.pkgNs) {
					t.Fatalf("error should mention both namespaces (%q and %q), got: %v", tc.pkgNs, tc.envNs, err)
				}
			} else if err != nil {
				t.Fatalf("expected acceptance, got: %v", err)
			}
		})
	}
}
