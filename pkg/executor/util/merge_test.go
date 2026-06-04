// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"reflect"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// dropAllSC is the SecurityContext that sanitizeContainerSecurityContext
// produces for an input with no SecurityContext at all — Capabilities.Drop is
// forced to ["ALL"] (GHSA-qf5v-m7p4-95rp). Test fixtures that merge two
// matching containers must expect this in the result.
func dropAllSC() *apiv1.SecurityContext {
	return &apiv1.SecurityContext{
		Capabilities: &apiv1.Capabilities{
			Drop: []apiv1.Capability{"ALL"},
		},
	}
}

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
				SecurityContext:          dropAllSC(),
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

// TestMergeContainer_SanitizesSecurityContext pins the GHSA-m63v-2g9w-2w6v
// invariant: MergeContainer is the path for the Environment Runtime.Container /
// Builder.Container fields, which do not go through any PodSpec and so are not
// reached by MergePodSpec's sanitizer. A tenant-supplied container with
// privileged=true / allowPrivilegeEscalation=true / dangerous caps must be
// sanitized in the merged result, while the caller's source container must not
// be mutated (it is typically env.Spec.Runtime.Container from an informer
// cache).
func TestMergeContainer_SanitizesSecurityContext(t *testing.T) {
	on := true
	dst := &apiv1.Container{Name: "py", Image: "fission/python-env:latest"}
	src := &apiv1.Container{
		Name: "py",
		SecurityContext: &apiv1.SecurityContext{
			Privileged:               &on,
			AllowPrivilegeEscalation: &on,
			Capabilities: &apiv1.Capabilities{
				Add: []apiv1.Capability{"SYS_ADMIN", "NET_BIND_SERVICE", "NET_ADMIN"},
			},
		},
	}

	out, err := MergeContainer(dst, src)
	if err != nil {
		t.Fatalf("MergeContainer error: %v", err)
	}
	if out.SecurityContext == nil {
		t.Fatalf("merged container must keep a SecurityContext")
	}
	if out.SecurityContext.Privileged != nil && *out.SecurityContext.Privileged {
		t.Errorf("Privileged=true must be sanitized to false")
	}
	if out.SecurityContext.AllowPrivilegeEscalation != nil && *out.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("AllowPrivilegeEscalation=true must be sanitized to false")
	}
	gotCaps := map[apiv1.Capability]bool{}
	for _, c := range out.SecurityContext.Capabilities.Add {
		gotCaps[c] = true
	}
	if gotCaps["SYS_ADMIN"] || gotCaps["NET_ADMIN"] {
		t.Errorf("non-allowlisted capabilities must be stripped, got %v", out.SecurityContext.Capabilities.Add)
	}
	if !gotCaps["NET_BIND_SERVICE"] {
		t.Errorf("allowlisted capability NET_BIND_SERVICE must flow through")
	}
	// GHSA-qf5v: drop must be forced to ["ALL"] to neutralize OCI default caps.
	if got := out.SecurityContext.Capabilities.Drop; len(got) != 1 || got[0] != "ALL" {
		t.Errorf("capabilities.drop must be [ALL], got %v", got)
	}

	// The caller's source container must not be mutated by the merge.
	if src.SecurityContext.Privileged == nil || !*src.SecurityContext.Privileged {
		t.Errorf("source container Privileged must be left untouched (deep copy expected)")
	}
	if len(src.SecurityContext.Capabilities.Add) != 3 {
		t.Errorf("source container capabilities must be left untouched, got %v", src.SecurityContext.Capabilities.Add)
	}
	if src.SecurityContext.Capabilities.Drop != nil {
		t.Errorf("source container Capabilities.Drop must remain nil (deep copy expected), got %v", src.SecurityContext.Capabilities.Drop)
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
				{Name: "foo", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}, {Name: "test", Value: "foobar"}}, SecurityContext: dropAllSC()},
				{Name: "foo2", Image: "my-custom-image-2", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}, {Name: "env3", Value: "foobar"}, {Name: "env4", Value: "barfoo"}}, SecurityContext: dropAllSC()},
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
				{Name: "foo", Image: "my-custom-image-1", Env: []apiv1.EnvVar{{Name: "env1", Value: "foobar"}, {Name: "env2", Value: "barfoo"}, {Name: "test", Value: "foobar"}}, SecurityContext: dropAllSC()},
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
// privileged=true / allowPrivilegeEscalation=true / non-allowlisted
// capabilities must be stripped from the merged result, and
// capabilities.drop must be forced to ["ALL"] so the OCI runtime's
// ~14 default capabilities are not granted either. The webhook is
// the primary defence; this layer makes the bits unreachable on
// webhook-bypass clusters. Closes GHSA-gx55 / GHSA-wmgg / GHSA-v455
// / GHSA-qf5v.
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
						"SYS_ADMIN",        // denylist holdover — stripped
						"SYS_TIME",         // GHSA-qf5v exemplar — stripped
						"NET_BIND_SERVICE", // allowlisted — flows through
						"NET_ADMIN",        // stripped
						"CHOWN",            // OCI default; not in allowlist — stripped
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
	// Only NET_BIND_SERVICE is on the allowlist; everything else must be stripped.
	gotAdd := map[apiv1.Capability]bool{}
	for _, cap := range merged.SecurityContext.Capabilities.Add {
		gotAdd[cap] = true
	}
	for _, cap := range []apiv1.Capability{"SYS_ADMIN", "SYS_TIME", "NET_ADMIN", "CHOWN"} {
		if gotAdd[cap] {
			t.Errorf("non-allowlisted capability %q must be stripped, got %v", cap, merged.SecurityContext.Capabilities.Add)
		}
	}
	if !gotAdd["NET_BIND_SERVICE"] {
		t.Errorf("allowlisted capability NET_BIND_SERVICE must flow through")
	}
	// GHSA-qf5v: drop must be forced to ["ALL"].
	if got := merged.SecurityContext.Capabilities.Drop; len(got) != 1 || got[0] != "ALL" {
		t.Errorf("capabilities.drop must be [ALL], got %v", got)
	}

	// InitContainer must also be sanitized, including drop=["ALL"] even though
	// the source had no Capabilities at all.
	initSC := out.InitContainers[0].SecurityContext
	if initSC.Privileged != nil && *initSC.Privileged {
		t.Errorf("InitContainer privileged=true must be sanitized to false")
	}
	if initSC.Capabilities == nil {
		t.Fatalf("InitContainer Capabilities must be allocated to carry drop=[ALL]")
	}
	if got := initSC.Capabilities.Drop; len(got) != 1 || got[0] != "ALL" {
		t.Errorf("InitContainer capabilities.drop must be [ALL], got %v", got)
	}
}

// TestMergePodSpec_AllocatesSecurityContextForDropAll asserts that even a
// container that arrived with no SecurityContext at all (and so no requested
// caps) still leaves the merge with drop: ["ALL"] applied. Without this, the
// OCI runtime would grant its ~14 default capabilities — the structural gap
// the GHSA-qf5v denylist could never close.
func TestMergePodSpec_AllocatesSecurityContextForDropAll(t *testing.T) {
	src := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "user", Image: "fission/python-env:latest"}},
	}
	target := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "user"}}, // no SecurityContext at all
	}
	out, _ := MergePodSpec(src, target)
	if out.Containers[0].SecurityContext == nil {
		t.Fatalf("SecurityContext must be allocated to carry drop=[ALL]")
	}
	if out.Containers[0].SecurityContext.Capabilities == nil {
		t.Fatalf("Capabilities must be allocated to carry drop=[ALL]")
	}
	if got := out.Containers[0].SecurityContext.Capabilities.Drop; len(got) != 1 || got[0] != "ALL" {
		t.Errorf("capabilities.drop must be [ALL] even for containers without a SecurityContext, got %v", got)
	}
}
