// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestApplyEnvRuntimeClassFillsWhenNil(t *testing.T) {
	env := &fv1.Environment{
		Spec: fv1.EnvironmentSpec{
			RuntimeClassName: new("gvisor"),
		},
	}
	podSpec := &apiv1.PodSpec{}

	ApplyEnvRuntimeClass(podSpec, env)

	require.NotNil(t, podSpec.RuntimeClassName)
	assert.Equal(t, "gvisor", *podSpec.RuntimeClassName)
}

func TestApplyEnvRuntimeClassDoesNotOverridePodSpecValue(t *testing.T) {
	env := &fv1.Environment{
		Spec: fv1.EnvironmentSpec{
			RuntimeClassName: new("gvisor"),
		},
	}
	podSpec := &apiv1.PodSpec{
		RuntimeClassName: new("kata"),
	}

	ApplyEnvRuntimeClass(podSpec, env)

	require.NotNil(t, podSpec.RuntimeClassName)
	assert.Equal(t, "kata", *podSpec.RuntimeClassName,
		"a RuntimeClassName already present on the pod spec must win over the env-level field")
}

func TestApplyEnvRuntimeClassNoopWhenEnvFieldNil(t *testing.T) {
	env := &fv1.Environment{
		Spec: fv1.EnvironmentSpec{},
	}
	podSpec := &apiv1.PodSpec{}

	ApplyEnvRuntimeClass(podSpec, env)

	assert.Nil(t, podSpec.RuntimeClassName)
}
