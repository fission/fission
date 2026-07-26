// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"testing"

	"github.com/stretchr/testify/assert"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestSnapshotEnvNamespace(t *testing.T) {
	tests := []struct {
		name        string
		envNS       string
		fnNamespace string
		want        string
	}{
		{
			name:        "explicit snapshot namespace wins",
			envNS:       "envs-ns",
			fnNamespace: "default",
			want:        "envs-ns",
		},
		{
			name:        "empty snapshot namespace falls back to function namespace",
			envNS:       "",
			fnNamespace: "default",
			want:        "default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &fv1.FunctionVersion{
				Spec: fv1.FunctionVersionSpec{
					Snapshot: fv1.FunctionSpec{
						Environment: fv1.EnvironmentReference{
							Name:      "node",
							Namespace: tt.envNS,
						},
					},
				},
			}
			assert.Equal(t, tt.want, SnapshotEnvNamespace(v, tt.fnNamespace))
		})
	}
}

func TestEnvDrifted(t *testing.T) {
	tests := []struct {
		name        string
		observedGen int64
		liveGen     int64
		want        bool
	}{
		{
			name:        "equal generations means no drift",
			observedGen: 3,
			liveGen:     3,
			want:        false,
		},
		{
			name:        "unequal generations means drift",
			observedGen: 1,
			liveGen:     2,
			want:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &fv1.FunctionVersion{
				Spec: fv1.FunctionVersionSpec{EnvObservedGeneration: tt.observedGen},
			}
			env := &fv1.Environment{}
			env.Generation = tt.liveGen
			assert.Equal(t, tt.want, EnvDrifted(v, env))
		})
	}
}
