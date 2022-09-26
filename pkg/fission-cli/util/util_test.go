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
	"fmt"
	"reflect"
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

func TestGetConfig(t *testing.T) {
	response, err := GetKubernetesCurrentNamespace("")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("Current NS: ", response)
}
