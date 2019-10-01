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
		objs interface{}
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "container name",
			args: args{
				[]apiv1.Container{
					{
						Name: "test1",
					},
					{
						Name: "test2",
					},
					{
						Name: "test3",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "pass non-slice",
			args: args{
				apiv1.Container{
					Name: "test1",
				},
			},
			wantErr: true,
		},
		{
			name: "conflict container name",
			args: args{
				[]interface{}{
					apiv1.Container{
						Name: "test1",
					},
					apiv1.Container{
						Name: "test1",
					},
					apiv1.Container{
						Name: "test3",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "different types",
			args: args{
				[]interface{}{
					apiv1.VolumeMount{
						Name: "test1",
					},
					apiv1.EnvFromSource{
						Prefix:       "",
						ConfigMapRef: nil,
						SecretRef:    nil,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "type without target field",
			args: args{
				[]interface{}{
					apiv1.EnvFromSource{
						Prefix:       "",
						ConfigMapRef: nil,
						SecretRef:    nil,
					},
				},
			},
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
					Name:       "test",
					Image:      "foobar",
					Command:    []string{"a"},
					Args:       []string{"b"},
					WorkingDir: "/tmp",
					Ports: []apiv1.ContainerPort{
						{
							Name:     "http1",
							HostPort: 123,
						},
					},
					EnvFrom: []apiv1.EnvFromSource{
						{
							Prefix: "asd",
						},
					},
					Env: []apiv1.EnvVar{
						{
							Name:  "foobar",
							Value: "dummy",
						},
					},
					Resources: apiv1.ResourceRequirements{
						Limits: apiv1.ResourceList{
							apiv1.ResourceCPU: resource.Quantity{
								Format: "limit",
							},
						},
					},
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "volm",
							ReadOnly:  true,
							MountPath: "/tmp/foobar",
						},
					},
					VolumeDevices: []apiv1.VolumeDevice{
						{
							Name:       "vold",
							DevicePath: "hello",
						},
					},
					LivenessProbe: &apiv1.Probe{
						Handler:             apiv1.Handler{},
						InitialDelaySeconds: 1,
						TimeoutSeconds:      2,
						PeriodSeconds:       3,
						SuccessThreshold:    4,
						FailureThreshold:    5,
					},
					ReadinessProbe: &apiv1.Probe{
						Handler:             apiv1.Handler{},
						InitialDelaySeconds: 1,
						TimeoutSeconds:      2,
						PeriodSeconds:       3,
						SuccessThreshold:    4,
						FailureThreshold:    5,
					},
					Lifecycle:                nil,
					TerminationMessagePath:   "",
					TerminationMessagePolicy: "",
					ImagePullPolicy:          "IfNotPresent",
					SecurityContext:          nil,
					Stdin:                    false,
					StdinOnce:                false,
					TTY:                      false,
				},
				src: &apiv1.Container{
					Name:       "test",
					Image:      "foobar-1",
					Command:    []string{"a", "c"},
					Args:       []string{"b", "d"},
					WorkingDir: "/tmp/qwer",
					Ports: []apiv1.ContainerPort{
						{
							Name:     "http2",
							HostPort: 123,
						},
					},
					EnvFrom: []apiv1.EnvFromSource{
						{
							Prefix: "asd",
						},
					},
					Env: []apiv1.EnvVar{
						{
							Name:  "foobar1",
							Value: "dummy",
						},
					},
					Resources: apiv1.ResourceRequirements{
						Limits: apiv1.ResourceList{
							apiv1.ResourceCPU: resource.Quantity{
								Format: "unlimit",
							},
							apiv1.ResourceMemory: resource.Quantity{
								Format: "limit",
							},
						},
					},
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "volm1",
							ReadOnly:  true,
							MountPath: "/tmp/foobar",
						},
					},
					VolumeDevices: []apiv1.VolumeDevice{
						{
							Name:       "vold1",
							DevicePath: "hello",
						},
					},
					LivenessProbe: &apiv1.Probe{
						Handler:             apiv1.Handler{},
						InitialDelaySeconds: 5,
						TimeoutSeconds:      4,
						PeriodSeconds:       3,
						SuccessThreshold:    2,
						FailureThreshold:    1,
					},
					ReadinessProbe: &apiv1.Probe{
						Handler:             apiv1.Handler{},
						InitialDelaySeconds: 5,
						TimeoutSeconds:      4,
						PeriodSeconds:       3,
						SuccessThreshold:    2,
						FailureThreshold:    1,
					},
					Lifecycle:                nil,
					TerminationMessagePath:   "",
					TerminationMessagePolicy: "",
					ImagePullPolicy:          "Always",
					SecurityContext:          nil,
					Stdin:                    false,
					StdinOnce:                false,
					TTY:                      false,
				},
			},
			want: &apiv1.Container{
				Name:       "test",
				Image:      "foobar-1",
				Command:    []string{"a", "a", "c"},
				Args:       []string{"b", "b", "d"},
				WorkingDir: "/tmp/qwer",
				Ports: []apiv1.ContainerPort{
					{
						Name:     "http1",
						HostPort: 123,
					},
					{
						Name:     "http2",
						HostPort: 123,
					},
				},
				EnvFrom: []apiv1.EnvFromSource{
					{
						Prefix: "asd",
					},
					{
						Prefix: "asd",
					},
				},
				Env: []apiv1.EnvVar{
					{
						Name:  "foobar",
						Value: "dummy",
					},
					{
						Name:  "foobar1",
						Value: "dummy",
					},
				},
				Resources: apiv1.ResourceRequirements{
					Limits: apiv1.ResourceList{
						apiv1.ResourceCPU: resource.Quantity{
							Format: "unlimit",
						},
						apiv1.ResourceMemory: resource.Quantity{
							Format: "limit",
						},
					},
				},
				VolumeMounts: []apiv1.VolumeMount{
					{
						Name:      "volm",
						ReadOnly:  true,
						MountPath: "/tmp/foobar",
					},
					{
						Name:      "volm1",
						ReadOnly:  true,
						MountPath: "/tmp/foobar",
					},
				},
				VolumeDevices: []apiv1.VolumeDevice{
					{
						Name:       "vold",
						DevicePath: "hello",
					},
					{
						Name:       "vold1",
						DevicePath: "hello",
					},
				},
				LivenessProbe: &apiv1.Probe{
					Handler:             apiv1.Handler{},
					InitialDelaySeconds: 5,
					TimeoutSeconds:      4,
					PeriodSeconds:       3,
					SuccessThreshold:    2,
					FailureThreshold:    1,
				},
				ReadinessProbe: &apiv1.Probe{
					Handler:             apiv1.Handler{},
					InitialDelaySeconds: 5,
					TimeoutSeconds:      4,
					PeriodSeconds:       3,
					SuccessThreshold:    2,
					FailureThreshold:    1,
				},
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
