// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"strings"
	"testing"

	apiv1 "k8s.io/api/core/v1"
)

func TestValidatePodSpecSafety_Nil(t *testing.T) {
	if err := ValidatePodSpecSafety("Function.spec.podspec", nil); err != nil {
		t.Fatalf("nil podspec must be accepted, got: %v", err)
	}
}

func TestValidatePodSpecSafety_Benign(t *testing.T) {
	allow := false
	ps := &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name:    "user",
			Image:   "alpine:3.19",
			Command: []string{"/bin/sh", "-c", "echo hi"},
			Env:     []apiv1.EnvVar{{Name: "FOO", Value: "bar"}},
			SecurityContext: &apiv1.SecurityContext{
				AllowPrivilegeEscalation: &allow,
				Capabilities: &apiv1.Capabilities{
					Add: []apiv1.Capability{"NET_BIND_SERVICE"},
				},
			},
		}},
		Volumes: []apiv1.Volume{{
			Name: "cm",
			VolumeSource: apiv1.VolumeSource{
				ConfigMap: &apiv1.ConfigMapVolumeSource{
					LocalObjectReference: apiv1.LocalObjectReference{Name: "my-cm"},
				},
			},
		}},
		NodeSelector: map[string]string{"role": "fn"},
	}
	if err := ValidatePodSpecSafety("Function.spec.podspec", ps); err != nil {
		t.Fatalf("benign podspec must be accepted, got: %v", err)
	}
}

func TestValidatePodSpecSafety_DangerousFields(t *testing.T) {
	on := true
	cases := []struct {
		name      string
		mutate    func(*apiv1.PodSpec)
		wantInErr string
	}{
		{
			name:      "hostNetwork",
			mutate:    func(ps *apiv1.PodSpec) { ps.HostNetwork = true },
			wantInErr: "hostNetwork",
		},
		{
			name:      "hostPID",
			mutate:    func(ps *apiv1.PodSpec) { ps.HostPID = true },
			wantInErr: "hostPID",
		},
		{
			name:      "hostIPC",
			mutate:    func(ps *apiv1.PodSpec) { ps.HostIPC = true },
			wantInErr: "hostIPC",
		},
		{
			name:      "serviceAccountName override",
			mutate:    func(ps *apiv1.PodSpec) { ps.ServiceAccountName = "cluster-admin" },
			wantInErr: "serviceAccountName",
		},
		{
			name:      "deprecated serviceAccount (alias) override",
			mutate:    func(ps *apiv1.PodSpec) { ps.DeprecatedServiceAccount = "cluster-admin" },
			wantInErr: "serviceAccount",
		},
		{
			name: "hostPath volume",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Volumes = []apiv1.Volume{{
					Name: "host-root",
					VolumeSource: apiv1.VolumeSource{
						HostPath: &apiv1.HostPathVolumeSource{Path: "/"},
					},
				}}
			},
			wantInErr: "hostPath",
		},
		{
			name: "privileged container",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name:            "user",
					SecurityContext: &apiv1.SecurityContext{Privileged: &on},
				}}
			},
			wantInErr: "privileged",
		},
		{
			name: "allowPrivilegeEscalation=true",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name:            "user",
					SecurityContext: &apiv1.SecurityContext{AllowPrivilegeEscalation: &on},
				}}
			},
			wantInErr: "allowPrivilegeEscalation",
		},
		{
			name: "SYS_ADMIN capability",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{
							Add: []apiv1.Capability{"SYS_ADMIN"},
						},
					},
				}}
			},
			wantInErr: "SYS_ADMIN",
		},
		{
			name: "NET_ADMIN capability",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{
							Add: []apiv1.Capability{"NET_ADMIN"},
						},
					},
				}}
			},
			wantInErr: "NET_ADMIN",
		},
		{
			name: "privileged init container",
			mutate: func(ps *apiv1.PodSpec) {
				ps.InitContainers = []apiv1.Container{{
					Name:            "init",
					SecurityContext: &apiv1.SecurityContext{Privileged: &on},
				}}
			},
			wantInErr: "initContainers",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := &apiv1.PodSpec{Containers: []apiv1.Container{{Name: "user"}}}
			tc.mutate(ps)
			err := ValidatePodSpecSafety("Function.spec.podspec", ps)
			if err == nil {
				t.Fatalf("expected rejection for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error must mention %q, got: %v", tc.wantInErr, err)
			}
			if !strings.Contains(err.Error(), "Function.spec.podspec") {
				t.Fatalf("error must include the field prefix, got: %v", err)
			}
		})
	}
}

// TestValidateContainerSafety covers the standalone-container check used for
// Environment Runtime.Container / Builder.Container. Closes GHSA-m63v-2g9w-2w6v.
func TestValidateContainerSafety(t *testing.T) {
	on := true
	off := false

	t.Run("nil container is accepted", func(t *testing.T) {
		if err := ValidateContainerSafety("Environment.spec.runtime.container", nil); err != nil {
			t.Fatalf("nil container must be accepted, got: %v", err)
		}
	})

	t.Run("nil securityContext is accepted", func(t *testing.T) {
		c := &apiv1.Container{Name: "py", Image: "fission/python-env:latest"}
		if err := ValidateContainerSafety("Environment.spec.runtime.container", c); err != nil {
			t.Fatalf("container without securityContext must be accepted, got: %v", err)
		}
	})

	t.Run("benign securityContext is accepted", func(t *testing.T) {
		c := &apiv1.Container{
			Name: "py",
			SecurityContext: &apiv1.SecurityContext{
				AllowPrivilegeEscalation: &off,
				Capabilities:             &apiv1.Capabilities{Add: []apiv1.Capability{"NET_BIND_SERVICE"}},
			},
		}
		if err := ValidateContainerSafety("Environment.spec.runtime.container", c); err != nil {
			t.Fatalf("benign container must be accepted, got: %v", err)
		}
	})

	cases := []struct {
		name      string
		sc        *apiv1.SecurityContext
		wantInErr string
	}{
		{"privileged", &apiv1.SecurityContext{Privileged: &on}, "privileged"},
		{"allowPrivilegeEscalation", &apiv1.SecurityContext{AllowPrivilegeEscalation: &on}, "allowPrivilegeEscalation"},
		{"SYS_ADMIN", &apiv1.SecurityContext{Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"SYS_ADMIN"}}}, "SYS_ADMIN"},
		{"NET_ADMIN", &apiv1.SecurityContext{Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"NET_ADMIN"}}}, "NET_ADMIN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &apiv1.Container{Name: "py", SecurityContext: tc.sc}
			err := ValidateContainerSafety("Environment.spec.runtime.container", c)
			if err == nil {
				t.Fatalf("expected rejection for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error must mention %q, got: %v", tc.wantInErr, err)
			}
			if !strings.Contains(err.Error(), "Environment.spec.runtime.container") {
				t.Fatalf("error must include the field prefix, got: %v", err)
			}
		})
	}
}

// TestValidatePodSpecSafety_BenignCapability asserts that NET_BIND_SERVICE
// (and other non-dangerous capabilities) flow through. The denylist is
// intentionally narrow so legitimate function workloads can still bind to
// privileged ports etc.
func TestValidatePodSpecSafety_BenignCapability(t *testing.T) {
	ps := &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name: "user",
			SecurityContext: &apiv1.SecurityContext{
				Capabilities: &apiv1.Capabilities{
					Add: []apiv1.Capability{"NET_BIND_SERVICE", "CHOWN"},
				},
			},
		}},
	}
	if err := ValidatePodSpecSafety("Function.spec.podspec", ps); err != nil {
		t.Fatalf("non-dangerous capabilities must flow through, got: %v", err)
	}
}
