// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			Name: name, Key: key}}
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
			{SecretRef: &apiv1.SecretEnvSource{Name: "s"}}}},
		{name: "envFrom with neither ref rejected", envFrom: []apiv1.EnvFromSource{{}}, wantErr: true},
		{name: "envFrom with both refs rejected", envFrom: []apiv1.EnvFromSource{{
			SecretRef:    &apiv1.SecretEnvSource{Name: "s"},
			ConfigMapRef: &apiv1.ConfigMapEnvSource{Name: "c"},
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

	// The webhook calls ValidateForAdmission, NOT Validate — a rule that only
	// Validate runs is enforced on the CLI path alone and bypassed by
	// kubectl/GitOps writers. Every RFC-0030 rule must survive this path.
	t.Run("rules are enforced on the admission path the webhook uses", func(t *testing.T) {
		t.Parallel()
		fn := Function{
			Name: "f", Namespace: "default",
			Spec: newdeploySpec(),
		}
		fn.Spec.Env = []apiv1.EnvVar{{Name: "FISSION_STATE_URL", Value: "http://evil"}}
		require.Error(t, fn.ValidateForAdmission(), "reserved name must be rejected at admission")

		fn.Spec.Env = []apiv1.EnvVar{{Name: "POD", ValueFrom: &apiv1.EnvVarSource{
			FieldRef: &apiv1.ObjectFieldSelector{FieldPath: "metadata.name"}}}}
		require.Error(t, fn.ValidateForAdmission(), "downward-API valueFrom must be rejected at admission")

		fn.Spec.Env = []apiv1.EnvVar{{Name: "DATABASE_URL", Value: "postgres://db"}}
		fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = ExecutorTypePoolmgr
		require.Error(t, fn.ValidateForAdmission(), "poolmgr gate must be rejected at admission")

		fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = ExecutorTypeNewdeploy
		fn.Spec.Secrets = []SecretReference{{Namespace: "default", Name: "s", MountPath: "/etc/app"}}
		require.Error(t, fn.ValidateForAdmission(), "an absolute mountPath must be rejected at admission")

		fn.Spec.Secrets = []SecretReference{{Namespace: "default", Name: "s", MountPath: "app"}}
		require.NoError(t, fn.ValidateForAdmission(), "a relative mountPath is supported now that files mode has landed")

		fn.Spec.Secrets = nil
		require.NoError(t, fn.ValidateForAdmission(), "a valid newdeploy function must pass")
	})

	t.Run("container executor rejects envFrom alongside legacy references", func(t *testing.T) {
		t.Parallel()
		s := newdeploySpec()
		s.Env = nil
		s.InvokeStrategy.ExecutionStrategy.ExecutorType = ExecutorTypeContainer
		s.PodSpec = &apiv1.PodSpec{Containers: []apiv1.Container{{Name: "c", Image: "img"}}}
		s.EnvFrom = []apiv1.EnvFromSource{{
			SecretRef: &apiv1.SecretEnvSource{Name: "creds"}}}
		require.NoError(t, s.validateEnvForAdmission())

		// The executor skips the legacy synthesis when EnvFrom is set, so
		// carrying both would silently drop the legacy projection.
		s.Secrets = []SecretReference{{Namespace: "default", Name: "legacy"}}
		require.Error(t, s.validateEnvForAdmission())
	})

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

	// Files mode has landed, so a relative mountPath is accepted and only the
	// unmaterialisable shapes are refused. Full rules: TestValidateMountPaths.
	t.Run("mountPath must be relative and confined", func(t *testing.T) {
		t.Parallel()
		s := newdeploySpec()
		s.Env = nil
		s.Secrets = []SecretReference{{Namespace: "ns", Name: "s", MountPath: "app/creds"}}
		assert.NoError(t, s.Validate(), "a relative mountPath is the supported shape")

		s.Secrets[0].MountPath = "/etc/app"
		assert.Error(t, s.Validate(), "an absolute mountPath is not materialisable under the shared root")

		s.Secrets[0].MountPath = ""
		assert.NoError(t, s.Validate())
	})
}

// TestTerminationGracePeriodWireContract pins the reason the field is a
// pointer: an explicit 0 must survive marshalling (the previous int64 +
// omitempty dropped it as absent, and the apiserver's CRD default served it
// back as 90 — "instant kill" was expressible only via raw YAML), while an
// unset field must still marshal as absent so the CRD default applies.
func TestTerminationGracePeriodWireContract(t *testing.T) {
	t.Parallel()

	explicitZero, err := json.Marshal(EnvironmentSpec{TerminationGracePeriod: new(int64(0))})
	require.NoError(t, err)
	assert.Contains(t, string(explicitZero), `"terminationGracePeriod":0`,
		"an explicit 0 must reach the wire — it is a real value, not the absence of one")

	unset, err := json.Marshal(EnvironmentSpec{})
	require.NoError(t, err)
	assert.NotContains(t, string(unset), "terminationGracePeriod",
		"nil must marshal as absent so the apiserver's CRD default fills it")
}

func TestEffectiveTerminationGracePeriod(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultTerminationGracePeriod, EnvironmentSpec{}.EffectiveTerminationGracePeriod(),
		"nil means: use the default")
	assert.Zero(t, EnvironmentSpec{TerminationGracePeriod: new(int64(0))}.EffectiveTerminationGracePeriod(),
		"an explicit 0 is 0, never the default")
	assert.Equal(t, int64(300), EnvironmentSpec{TerminationGracePeriod: new(int64(300))}.EffectiveTerminationGracePeriod())
}
