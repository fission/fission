// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
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

// func TestGetConfig(t *testing.T) {
// 	response, err := GetKubernetesNamespace("")
// 	if err != nil {
// 		t.Log(err)
// 	}
// 	t.Log("Current NS: ", response)
// }
