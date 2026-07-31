// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
)

func TestIsReservedEnvName(t *testing.T) {
	t.Parallel()
	reserved := []string{
		"FISSION_STATE_URL", "FISSION_ANYTHING", "RESOURCE_VERSION_COUNT",
		"HTTP_PROXY", "https_proxy", "NO_PROXY", "LD_PRELOAD", "NODE_OPTIONS", "PYTHONPATH",
	}
	for _, name := range reserved {
		assert.True(t, IsReservedEnvName(name), name)
	}
	allowed := []string{"DATABASE_URL", "OTEL_SERVICE_NAME", "PATH", "FISSIONX", "MY_FISSION_URL"}
	for _, name := range allowed {
		assert.False(t, IsReservedEnvName(name), name)
	}
}

func TestValidateFunctionEnv(t *testing.T) {
	t.Parallel()

	secretRef := func(name, key string) *apiv1.EnvVarSource {
		return &apiv1.EnvVarSource{SecretKeyRef: &apiv1.SecretKeySelector{
			LocalObjectReference: apiv1.LocalObjectReference{Name: name}, Key: key}}
	}

	cases := []struct {
		name    string
		env     []apiv1.EnvVar
		envFrom []apiv1.EnvFromSource
		wantErr bool
	}{
		{name: "literal and valueFrom accepted", env: []apiv1.EnvVar{
			{Name: "DATABASE_URL", Value: "postgres://db"},
			{Name: "TOKEN", ValueFrom: secretRef("creds", "token")},
		}},
		{name: "reserved literal rejected", env: []apiv1.EnvVar{
			{Name: "FISSION_STATE_URL", Value: "http://evil"}}, wantErr: true},
		{name: "proxy hijack rejected", env: []apiv1.EnvVar{
			{Name: "HTTPS_PROXY", Value: "http://evil"}}, wantErr: true},
		{name: "empty name rejected", env: []apiv1.EnvVar{{Value: "x"}}, wantErr: true},
		{name: "duplicate name rejected", env: []apiv1.EnvVar{
			{Name: "A", Value: "1"}, {Name: "A", Value: "2"}}, wantErr: true},
		{name: "fieldRef rejected", env: []apiv1.EnvVar{
			{Name: "POD_NAME", ValueFrom: &apiv1.EnvVarSource{
				FieldRef: &apiv1.ObjectFieldSelector{FieldPath: "metadata.name"}}}}, wantErr: true},
		{name: "resourceFieldRef rejected", env: []apiv1.EnvVar{
			{Name: "CPU", ValueFrom: &apiv1.EnvVarSource{
				ResourceFieldRef: &apiv1.ResourceFieldSelector{Resource: "limits.cpu"}}}}, wantErr: true},
		{name: "value and valueFrom together rejected", env: []apiv1.EnvVar{
			{Name: "A", Value: "x", ValueFrom: secretRef("creds", "k")}}, wantErr: true},
		{name: "empty valueFrom rejected", env: []apiv1.EnvVar{
			{Name: "A", ValueFrom: &apiv1.EnvVarSource{}}}, wantErr: true},
		{name: "envFrom secret accepted", envFrom: []apiv1.EnvFromSource{
			{SecretRef: &apiv1.SecretEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: "s"}}}}},
		{name: "envFrom with neither ref rejected", envFrom: []apiv1.EnvFromSource{{}}, wantErr: true},
		{name: "envFrom with both refs rejected", envFrom: []apiv1.EnvFromSource{{
			SecretRef:    &apiv1.SecretEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: "s"}},
			ConfigMapRef: &apiv1.ConfigMapEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: "c"}},
		}}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateFunctionEnv(tc.env, tc.envFrom)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestFunctionSpecEnvPhaseGates pins the RFC-0030 phase-1 gates: poolmgr (and
// empty executor type, which defaults to poolmgr) rejects env/envFrom until
// the specialize-payload phase lands, and a set MountPath is rejected rather
// than silently ignored until files mode lands.
func TestFunctionSpecEnvPhaseGates(t *testing.T) {
	t.Parallel()

	newdeploySpec := func() FunctionSpec {
		return FunctionSpec{
			Environment: EnvironmentReference{Namespace: "ns", Name: "env"},
			InvokeStrategy: InvokeStrategy{
				StrategyType: StrategyTypeExecution,
				ExecutionStrategy: ExecutionStrategy{
					ExecutorType: ExecutorTypeNewdeploy, MinScale: 0, MaxScale: 1,
				},
			},
			Env: []apiv1.EnvVar{{Name: "DATABASE_URL", Value: "postgres://db"}},
		}
	}

	t.Run("newdeploy with env passes", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, newdeploySpec().Validate())
	})

	t.Run("poolmgr with env rejected", func(t *testing.T) {
		t.Parallel()
		s := newdeploySpec()
		s.InvokeStrategy.ExecutionStrategy.ExecutorType = ExecutorTypePoolmgr
		assert.Error(t, s.Validate())
	})

	t.Run("empty executor type with env rejected (defaults to poolmgr)", func(t *testing.T) {
		t.Parallel()
		s := newdeploySpec()
		s.InvokeStrategy = InvokeStrategy{}
		assert.Error(t, s.Validate())
	})

	t.Run("mountPath rejected until files mode", func(t *testing.T) {
		t.Parallel()
		s := newdeploySpec()
		s.Env = nil
		s.Secrets = []SecretReference{{Namespace: "ns", Name: "s", MountPath: "app/creds"}}
		assert.Error(t, s.Validate())
		s.Secrets[0].MountPath = ""
		assert.NoError(t, s.Validate())
	})
}
