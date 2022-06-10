/*
Copyright 2022 The Fission Authors.

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
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
)

func TestGetEnvVarFromStringSlice(t *testing.T) {

	tests := []struct {
		name string
		args []string
		want []v1.EnvVar
	}{
		{
			name: "params-with-key-value",
			args: []string{"serverless=fission", "container=docker"},
			want: []v1.EnvVar{
				{
					Name:  "serverless",
					Value: "fission",
				},
				{
					Name:  "container",
					Value: "docker",
				},
			},
		},
		{
			name: "params-with-nil-value",
			args: []string{"serverless=", "container="},
			want: []v1.EnvVar{},
		},
		{
			name: "params-with-only-key",
			args: []string{"serverless", "container"},
			want: []v1.EnvVar{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetEnvVarFromStringSlice(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetEnvVarFromStringSlice() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetStorageURL(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		want    string
		wantErr bool
	}{
		{
			name:    "Correct kubecontext",
			arg:     "",
			want:    "http://127.0.0.1:",
			wantErr: false,
		},
		{
			name:    "Wrong kubecontext",
			arg:     "test",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetStorageURL(tt.arg)
			var gotstring string
			if got == nil {
				gotstring = ""
			} else {
				gotstring = got.String()

			}
			if (err != nil) != tt.wantErr {
				t.Errorf("GetStorageURL() Error got = %v wanterr = %v", err, tt.wantErr)
				return
			}
			if !strings.Contains(gotstring, tt.want) {
				t.Errorf("GetStorageURL() = %v which must contain %v", got.String(), tt.want)
			}
		})
	}
}
