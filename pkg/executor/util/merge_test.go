/*
Copyright 2016 The Fission Authors.

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
package util

import (
	"reflect"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func Test_checkConflicts(t *testing.T) {
	type args struct {
		objs any
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name:    "container name",
			args:    args{[]apiv1.Container{{Name: "test1"}, {Name: "test2"}, {Name: "test3"}}},
			wantErr: false,
		},
		{
			name:    "pass non-slice",
			args:    args{apiv1.Container{Name: "test1"}},
			wantErr: true,
		},
		{
			name:    "conflict container name",
			args:    args{[]any{apiv1.Container{Name: "test1"}, apiv1.Container{Name: "test1"}, apiv1.Container{Name: "test3"}}},
			wantErr: true,
		},
		{
			name:    "different types",
			args:    args{[]any{apiv1.VolumeMount{Name: "test1"}, apiv1.EnvFromSource{Prefix: "", ConfigMapRef: nil, SecretRef: nil}}},
			wantErr: true,
		},
		{
			name:    "type without target field",
			args:    args{[]any{apiv1.EnvFromSource{Prefix: "", ConfigMapRef: nil, SecretRef: nil}}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkSliceConflicts("Name", tt.args.objs); (err != nil) != tt.wantErr {
				t.Errorf("checkNameConflict() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_mergeContainer(t *testing.T) {
	type args struct {
		dst *apiv1.Container
		src *apiv1.Container
	}
	tests := []struct {
		name    string
		args    args
		want    *apiv1.Container
		wantErr bool
	}{
		{
			name: "nil-src",
			args: args{
				dst: &apiv1.Container{
					Name: "test",
				},
				src: nil,
			},
			want: &apiv1.Container{
				Name: "test",
			},
			wantErr: false,
		},
		{
			name: "normal-merge",
			args: args{
				dst: &apiv1.Container{
					Name:            "test",
					Image:           "foobar",
					Command:         []string{"a"},
					Args:            []string{"b"},
					WorkingDir:      "/tmp",
					Ports:           []apiv1.ContainerPort{{Name: "http1", HostPort: 123}},
					EnvFrom:         []apiv1.EnvFromSource{{Prefix: "asd"}},
					Env:             []apiv1.EnvVar{{Name: "foobar", Value: "dummy"}},
					Resources:       apiv1.ResourceRequirements{Limits: apiv1.ResourceList{apiv1.ResourceCPU: resource.Quantity{Format: "limit"}}},
					VolumeMounts:    []apiv1.VolumeMount{{Name: "volm", ReadOnly: true, MountPath: "/tmp/foobar"}},
					VolumeDevices:   []apiv1.VolumeDevice{{Name: "vold", DevicePath: "hello"}},
					LivenessProbe:   &apiv1.Probe{ProbeHandler: apiv1.ProbeHandler{}, InitialDelaySeconds: 1, TimeoutSeconds: 2, PeriodSeconds: 3, SuccessThreshold: 4, FailureThreshold: 5},
					ReadinessProbe:  &apiv1.Probe{ProbeHandler: apiv1.ProbeHandler{}, InitialDelaySeconds: 1, TimeoutSeconds: 2, PeriodSeconds: 3, SuccessThreshold: 4, FailureThreshold: 5},
					ImagePullPolicy: "IfNotPresent",
				},
				src: &apiv1.Container{
					Name:            "test",
					Image:           "foobar-1",
					Command:         []string{"a", "c"},
					Args:            []string{"b", "d"},
					WorkingDir:      "/tmp/qwer",
					Ports:           []apiv1.ContainerPort{{Name: "http2", HostPort: 123}},
					EnvFrom:         []apiv1.EnvFromSource{{Prefix: "asd"}},
					Env:             []apiv1.EnvVar{{Name: "foobar1", Value: "dummy"}},
					Resources:       apiv1.ResourceRequirements{Limits: apiv1.ResourceList{apiv1.ResourceCPU: resource.Quantity{Format: "unlimit"}, apiv1.ResourceMemory: resource.Quantity{Format: "limit"}}},
					VolumeMounts:    []apiv1.VolumeMount{{Name: "volm1", ReadOnly: true, MountPath: "/tmp/foobar"}},
					VolumeDevices:   []apiv1.VolumeDevice{{Name: "vold1", DevicePath: "hello"}},
					LivenessProbe:   &apiv1.Probe{ProbeHandler: apiv1.ProbeHandler{}, InitialDelaySeconds: 5, TimeoutSeconds: 4, PeriodSeconds: 3, SuccessThreshold: 2, FailureThreshold: 1},
					ReadinessProbe:  &apiv1.Probe{ProbeHandler: apiv1.ProbeHandler{}, InitialDelaySeconds: 5, TimeoutSeconds: 4, PeriodSeconds: 3, SuccessThreshold: 2, FailureThreshold: 1},
					ImagePullPolicy: "Always",
				},
			},
			want: &apiv1.Container{
				Name:                     "test",
				Image:                    "foobar-1",
				Command:                  []string{"a", "a", "c"},
				Args:                     []string{"b", "b", "d"},
				WorkingDir:               "/tmp/qwer",
				Ports:                    []apiv1.ContainerPort{{Name: "http1", HostPort: 123}, {Name: "http2", HostPort: 123}},
				EnvFrom:                  []apiv1.EnvFromSource{{Prefix: "asd"}, {Prefix: "asd"}},
				Env:                      []apiv1.EnvVar{{Name: "foobar", Value: "dummy"}, {Name: "foobar1", Value: "dummy"}},
				Resources:                apiv1.ResourceRequirements{Limits: apiv1.ResourceList{apiv1.ResourceCPU: resource.Quantity{Format: "unlimit"}, apiv1.ResourceMemory: resource.Quantity{Format: "limit"}}},
				VolumeMounts:             []apiv1.VolumeMount{{Name: "volm", ReadOnly: true, MountPath: "/tmp/foobar"}, {Name: "volm1", ReadOnly: true, MountPath: "/tmp/foobar"}},
				VolumeDevices:            []apiv1.VolumeDevice{{Name: "vold", DevicePath: "hello"}, {Name: "vold1", DevicePath: "hello"}},
				LivenessProbe:            &apiv1.Probe{ProbeHandler: apiv1.ProbeHandler{}, InitialDelaySeconds: 5, TimeoutSeconds: 4, PeriodSeconds: 3, SuccessThreshold: 2, FailureThreshold: 1},
				ReadinessProbe:           &apiv1.Probe{ProbeHandler: apiv1.ProbeHandler{}, InitialDelaySeconds: 5, TimeoutSeconds: 4, PeriodSeconds: 3, SuccessThreshold: 2, FailureThreshold: 1},
				Lifecycle:                nil,
				TerminationMessagePath:   "",
				TerminationMessagePolicy: "",
				ImagePullPolicy:          "Always",
				SecurityContext:          nil,
				Stdin:                    false,
				StdinOnce:                false,
				TTY:                      false,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergeContainer(tt.args.dst, tt.args.src)
			if (err != nil) != tt.wantErr {
				t.Errorf("mergeContainer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mergeContainer() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_mergeVolumeLists(t *testing.T) {
	type args struct {
		dst []apiv1.Volume
		src []apiv1.Volume
	}
	tests := []struct {
		name    string
		args    args
		want    []apiv1.Volume
		wantErr bool
	}{
		{
			name: "merge volume list",
			args: args{
				dst: []apiv1.Volume{
					{Name: "vol1", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/foo"}}},
					{Name: "vol2", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/bar"}}},
				},
				src: []apiv1.Volume{
					{Name: "vol3", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/foobar"}}},
				},
			},
			want: []apiv1.Volume{
				{Name: "vol1", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/foo"}}},
				{Name: "vol2", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/bar"}}},
				{Name: "vol3", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/foobar"}}},
			},
			wantErr: false,
		},
		{
			name: "conflict volume name",
			args: args{
				dst: []apiv1.Volume{
					{Name: "vol1", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/foo"}}},
					{Name: "vol2", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/bar"}}},
				},
				src: []apiv1.Volume{
					{Name: "vol1", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/tmp/foobar"}}},
				},
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergeVolumeLists(tt.args.dst, tt.args.src)
			if (err != nil) != tt.wantErr {
				t.Errorf("mergeVolumeLists() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mergeVolumeLists() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_mergeContainerList(t *testing.T) {
	type args struct {
		dst []apiv1.Container
		src []apiv1.Container
	}
	tests := []struct {
		name    string
		args    args
		want    []apiv1.Container
		wantErr bool
	}{
		{
			name: "merge container with conflict name",
			args: args{
				dst: []apiv1.Container{
					{Name: "foo", Image: "dummy-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
					{Name: "foo2", Image: "dummy-image-2", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
				},
				src: []apiv1.Container{
					{Name: "foo", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "test", Value: "foobar"}}},
					{Name: "foo2", Image: "my-custom-image-2", Env: []apiv1.EnvVar{{Name: "env3", Value: "foobar"}, {Name: "env4", Value: "barfoo"}}},
				},
			},
			want: []apiv1.Container{
				{Name: "foo", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}, {Name: "test", Value: "foobar"}}},
				{Name: "foo2", Image: "my-custom-image-2", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}, {Name: "env3", Value: "foobar"}, {Name: "env4", Value: "barfoo"}}},
			},
			wantErr: false,
		},
		{
			name: "merge container with no conflict name",
			args: args{
				dst: []apiv1.Container{
					{Name: "foo", Image: "dummy-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
					{Name: "foo2", Image: "dummy-image-2", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
				},
				src: []apiv1.Container{
					{Name: "foo3", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "test", Value: "foobar"}}},
					{Name: "foo4", Image: "my-custom-image-2", Env: []apiv1.EnvVar{{Name: "env3", Value: "foobar"}, {Name: "env4", Value: "barfoo"}}},
				},
			},
			want: []apiv1.Container{
				{Name: "foo", Image: "dummy-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
				{Name: "foo2", Image: "dummy-image-2", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
				{Name: "foo3", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "test", Value: "foobar"}}},
				{Name: "foo4", Image: "my-custom-image-2", Env: []apiv1.EnvVar{{Name: "env3", Value: "foobar"}, {Name: "env4", Value: "barfoo"}}},
			},
			wantErr: false,
		},
		{
			name: "merge container with partial conflict name",
			args: args{
				dst: []apiv1.Container{
					{Name: "foo", Image: "dummy-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
					{Name: "foo2", Image: "dummy-image-2", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
				},
				src: []apiv1.Container{
					{Name: "foo", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "test", Value: "foobar"}}},
					{Name: "foo4", Image: "my-custom-image-2", Env: []apiv1.EnvVar{{Name: "env3", Value: "foobar"}, {Name: "env4", Value: "barfoo"}}},
				},
			},
			want: []apiv1.Container{
				{Name: "foo", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}, {Name: "test", Value: "foobar"}}},
				{Name: "foo2", Image: "dummy-image-2", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
				{Name: "foo4", Image: "my-custom-image-2", Env: []apiv1.EnvVar{{Name: "env3", Value: "foobar"}, {Name: "env4", Value: "barfoo"}}},
			},
			wantErr: false,
		},
		{
			name: "merge container with conflict environment variable",
			args: args{
				dst: []apiv1.Container{
					{Name: "foo", Image: "dummy-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}}},
				},
				src: []apiv1.Container{
					{Name: "foo", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "helloworld"}}},
				},
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergeContainerList(tt.args.dst, tt.args.src)
			if (err != nil) != tt.wantErr {
				t.Errorf("mergeInitContainerList() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			for _, obj := range got {
				match := false
				for _, w := range tt.want {
					if obj.Name == w.Name && reflect.DeepEqual(obj, w) {
						match = true
						break
					}
				}
				if !match {
					t.Errorf("mergeInitContainerList() got = %v, want %v", got, tt.want)
					break
				}
			}
		})
	}
}

// TestMergePodSpec_StripsDangerousFields pins the GHSA-gx55 / GHSA-wmgg /
// GHSA-v455 invariant on the merge layer: node-escape PodSpec fields supplied
// by a target (env.Spec.Runtime.PodSpec / Function.Spec.PodSpec / builder
// podSpecPatch) must NOT propagate onto the src spec. The admission webhook
// is the primary defence; this is belt-and-braces for clusters running with
// failurePolicy=Ignore or stale objects from a pre-webhook upgrade window.
//
// Pod-level SecurityContext IS propagated (the chart's runtimePodSpec /
// builderPodSpec features use it for operator hardening like runAsNonRoot /
// fsGroup / runAsUser=10001). The webhook denylist handles tenant-supplied
// per-container privileged / allowPrivilegeEscalation / dangerous-cap
// vectors which are the actual node-escape primitives.
func TestMergePodSpec_StripsDangerousFields(t *testing.T) {
	on := true
	runAsUser := int64(10001)
	runAsNonRoot := true
	src := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "user", Image: "fission/python-env:latest"}},
	}
	target := &apiv1.PodSpec{
		HostNetwork:        true,
		HostPID:            true,
		HostIPC:            true,
		ServiceAccountName: "cluster-admin",
		SecurityContext: &apiv1.PodSecurityContext{
			RunAsUser:    &runAsUser,
			RunAsNonRoot: &runAsNonRoot,
		},
		Volumes: []apiv1.Volume{{
			Name: "host-root",
			VolumeSource: apiv1.VolumeSource{
				HostPath: &apiv1.HostPathVolumeSource{Path: "/"},
			},
		}},
		Containers: []apiv1.Container{{
			Name:            "user",
			SecurityContext: &apiv1.SecurityContext{Privileged: &on},
		}},
	}

	out, _ := MergePodSpec(src, target)

	if out.HostNetwork {
		t.Errorf("HostNetwork must not propagate from target")
	}
	if out.HostPID {
		t.Errorf("HostPID must not propagate from target")
	}
	if out.HostIPC {
		t.Errorf("HostIPC must not propagate from target")
	}
	if out.ServiceAccountName != "" {
		t.Errorf("ServiceAccountName override must not propagate, got %q", out.ServiceAccountName)
	}
	// Pod-level SecurityContext MUST flow through to support operator
	// hardening from the chart's runtimePodSpec / builderPodSpec features.
	if out.SecurityContext == nil {
		t.Errorf("pod-level SecurityContext must propagate for operator hardening")
	} else if out.SecurityContext.RunAsUser == nil || *out.SecurityContext.RunAsUser != 10001 {
		t.Errorf("RunAsUser=10001 must propagate, got %+v", out.SecurityContext.RunAsUser)
	}
	for _, v := range out.Volumes {
		if v.HostPath != nil {
			t.Errorf("hostPath volume %q must not propagate", v.Name)
		}
	}
}

// TestMergePodSpec_SanitizesContainerSecurityContext pins the
// container-level defence-in-depth: even if admission was bypassed
// (failurePolicy=Ignore or stale objects), per-container
// privileged=true / allowPrivilegeEscalation=true / dangerous
// capabilities must be stripped from the merged result. The webhook
// is the primary defence; this layer makes the bits unreachable on
// webhook-bypass clusters. Closes GHSA-gx55 / GHSA-wmgg / GHSA-v455.
func TestMergePodSpec_SanitizesContainerSecurityContext(t *testing.T) {
	on := true
	src := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "user", Image: "fission/python-env:latest"}},
	}
	target := &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name: "user",
			SecurityContext: &apiv1.SecurityContext{
				Privileged:               &on,
				AllowPrivilegeEscalation: &on,
				Capabilities: &apiv1.Capabilities{
					Add: []apiv1.Capability{
						"SYS_ADMIN",
						"NET_BIND_SERVICE", // benign — must flow through
						"NET_ADMIN",
						"CHOWN", // benign — must flow through
					},
				},
			},
		}},
		InitContainers: []apiv1.Container{{
			Name: "init",
			SecurityContext: &apiv1.SecurityContext{
				Privileged: &on,
			},
		}},
	}

	out, _ := MergePodSpec(src, target)

	var merged *apiv1.Container
	for i := range out.Containers {
		if out.Containers[i].Name == "user" {
			merged = &out.Containers[i]
			break
		}
	}
	if merged == nil || merged.SecurityContext == nil {
		t.Fatalf("merged user container with SecurityContext expected")
	}
	if merged.SecurityContext.Privileged != nil && *merged.SecurityContext.Privileged {
		t.Errorf("Privileged=true must be sanitized to false")
	}
	if merged.SecurityContext.AllowPrivilegeEscalation != nil && *merged.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("AllowPrivilegeEscalation=true must be sanitized to false")
	}
	for _, cap := range merged.SecurityContext.Capabilities.Add {
		switch cap {
		case "SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE", "SYS_MODULE", "DAC_READ_SEARCH", "DAC_OVERRIDE":
			t.Errorf("dangerous capability %q must be stripped", cap)
		}
	}
	// Benign capabilities must remain.
	gotBenign := map[apiv1.Capability]bool{}
	for _, cap := range merged.SecurityContext.Capabilities.Add {
		gotBenign[cap] = true
	}
	if !gotBenign["NET_BIND_SERVICE"] {
		t.Errorf("benign capability NET_BIND_SERVICE must flow through")
	}
	if !gotBenign["CHOWN"] {
		t.Errorf("benign capability CHOWN must flow through")
	}

	// InitContainer must also be sanitized.
	if out.InitContainers[0].SecurityContext.Privileged != nil && *out.InitContainers[0].SecurityContext.Privileged {
		t.Errorf("InitContainer privileged=true must be sanitized to false")
	}
}
