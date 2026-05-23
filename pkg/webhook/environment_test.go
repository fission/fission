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

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

func makeValidEnvironment() *v1.Environment {
	return &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "py",
			Namespace: "default",
		},
		Spec: v1.EnvironmentSpec{
			Version: 2,
			Runtime: v1.Runtime{
				Image: "fission/python-env:latest",
			},
			Builder: v1.Builder{
				Image: "fission/python-builder:latest",
			},
		},
	}
}

func TestEnvironmentWebhook_Validate_Default(t *testing.T) {
	r := &Environment{}
	if err := r.Validate(makeValidEnvironment()); err != nil {
		t.Fatalf("baseline Environment must validate, got: %v", err)
	}
}

func TestEnvironmentWebhook_Validate_RejectsDangerousPodSpec(t *testing.T) {
	on := true
	cases := []struct {
		name      string
		mutate    func(*v1.Environment)
		wantInErr string
	}{
		{
			name: "runtime hostNetwork",
			mutate: func(e *v1.Environment) {
				e.Spec.Runtime.PodSpec = &apiv1.PodSpec{HostNetwork: true}
			},
			wantInErr: "hostNetwork",
		},
		{
			name: "runtime hostPath volume",
			mutate: func(e *v1.Environment) {
				e.Spec.Runtime.PodSpec = &apiv1.PodSpec{
					Volumes: []apiv1.Volume{{
						Name: "host-root",
						VolumeSource: apiv1.VolumeSource{
							HostPath: &apiv1.HostPathVolumeSource{Path: "/"},
						},
					}},
				}
			},
			wantInErr: "hostPath",
		},
		{
			name: "runtime privileged container",
			mutate: func(e *v1.Environment) {
				e.Spec.Runtime.PodSpec = &apiv1.PodSpec{
					Containers: []apiv1.Container{{
						Name:            "py",
						SecurityContext: &apiv1.SecurityContext{Privileged: &on},
					}},
				}
			},
			wantInErr: "privileged",
		},
		{
			name: "runtime SYS_ADMIN capability",
			mutate: func(e *v1.Environment) {
				e.Spec.Runtime.PodSpec = &apiv1.PodSpec{
					Containers: []apiv1.Container{{
						Name: "py",
						SecurityContext: &apiv1.SecurityContext{
							Capabilities: &apiv1.Capabilities{
								Add: []apiv1.Capability{"SYS_ADMIN"},
							},
						},
					}},
				}
			},
			wantInErr: "SYS_ADMIN",
		},
		{
			name: "builder hostPID",
			mutate: func(e *v1.Environment) {
				e.Spec.Builder.PodSpec = &apiv1.PodSpec{HostPID: true}
			},
			wantInErr: "hostPID",
		},
		{
			name: "builder serviceAccountName override",
			mutate: func(e *v1.Environment) {
				e.Spec.Builder.PodSpec = &apiv1.PodSpec{ServiceAccountName: "cluster-admin"}
			},
			wantInErr: "serviceAccountName",
		},
	}

	r := &Environment{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := makeValidEnvironment()
			tc.mutate(env)
			err := r.Validate(env)
			if err == nil {
				t.Fatalf("expected rejection for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error must mention %q, got: %v", tc.wantInErr, err)
			}
		})
	}
}

// TestEnvironmentWebhook_Validate_AcceptsBenignPodSpec ensures the new
// safety check doesn't over-reject. Legitimate fields like image,
// command, env, configmap volumes, NodeSelector, Tolerations, Resources
// must flow through.
func TestEnvironmentWebhook_Validate_AcceptsBenignPodSpec(t *testing.T) {
	env := makeValidEnvironment()
	env.Spec.Runtime.PodSpec = &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name:    "py",
			Image:   "fission/python-env:latest",
			Command: []string{"/bin/sh", "-c", "echo hi"},
			Env:     []apiv1.EnvVar{{Name: "DEBUG", Value: "true"}},
		}},
		NodeSelector: map[string]string{"role": "fn"},
		Tolerations:  []apiv1.Toleration{{Key: "dedicated", Operator: apiv1.TolerationOpEqual, Value: "fn"}},
		Volumes: []apiv1.Volume{{
			Name: "cm",
			VolumeSource: apiv1.VolumeSource{
				ConfigMap: &apiv1.ConfigMapVolumeSource{
					LocalObjectReference: apiv1.LocalObjectReference{Name: "my-cm"},
				},
			},
		}},
	}
	r := &Environment{}
	if err := r.Validate(env); err != nil {
		t.Fatalf("benign Runtime.PodSpec must be accepted, got: %v", err)
	}
}
