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

func TestApplyFunctionEnv(t *testing.T) {
	t.Parallel()

	platform := []apiv1.EnvVar{
		{Name: "RESOURCE_VERSION_COUNT", Value: "42"},
		{Name: "FISSION_STATE_URL", Value: "http://statesvc"},
	}
	basePodSpec := func() *apiv1.PodSpec {
		return &apiv1.PodSpec{Containers: []apiv1.Container{
			{
				Name: "runtime",
				Env: append([]apiv1.EnvVar{
					{Name: "RESOURCE_VERSION_COUNT", Value: "42"},
					{Name: "FISSION_STATE_URL", Value: "http://statesvc"},
					{Name: "FROM_ENV_PODSPEC", Value: "env-owned"},
				}, []apiv1.EnvVar{}...),
				EnvFrom: []apiv1.EnvFromSource{{
					ConfigMapRef: &apiv1.ConfigMapEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: "env-cm"}},
				}},
			},
			{Name: "fetcher", Env: []apiv1.EnvVar{{Name: "FETCHER_OWN", Value: "x"}}},
		}}
	}

	t.Run("no-op without new fields keeps the spec byte-identical", func(t *testing.T) {
		t.Parallel()
		spec, want := basePodSpec(), basePodSpec()
		ApplyFunctionEnv(spec, "runtime", &fv1.Function{}, platform)
		assert.Equal(t, want, spec)
	})

	t.Run("platform vars are re-appended last, user env in between", func(t *testing.T) {
		t.Parallel()
		spec := basePodSpec()
		fn := &fv1.Function{Spec: fv1.FunctionSpec{
			Env: []apiv1.EnvVar{{Name: "DATABASE_URL", Value: "postgres://db"}},
			EnvFrom: []apiv1.EnvFromSource{{
				SecretRef: &apiv1.SecretEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: "creds"}},
			}},
		}}
		ApplyFunctionEnv(spec, "runtime", fn, platform)

		runtime := spec.Containers[0]
		names := make([]string, 0, len(runtime.Env))
		for _, e := range runtime.Env {
			names = append(names, e.Name)
		}
		// Env-podspec entry first, user env after it, platform strictly last —
		// kubelet resolves duplicate names last-wins, so platform names win.
		assert.Equal(t, []string{"FROM_ENV_PODSPEC", "DATABASE_URL", "RESOURCE_VERSION_COUNT", "FISSION_STATE_URL"}, names)
		// Function EnvFrom appends after the environment podspec's sources
		// (later wins in k8s envFrom semantics, and env beats envFrom).
		require.Len(t, runtime.EnvFrom, 2)
		assert.Equal(t, "env-cm", runtime.EnvFrom[0].ConfigMapRef.Name)
		assert.Equal(t, "creds", runtime.EnvFrom[1].SecretRef.Name)
		// The sidecar container is untouched.
		assert.Equal(t, basePodSpec().Containers[1], spec.Containers[1])
	})

	t.Run("a user literal shadowing a platform name never ends up last", func(t *testing.T) {
		t.Parallel()
		spec := basePodSpec()
		fn := &fv1.Function{Spec: fv1.FunctionSpec{
			// Admission denies this, but admission is UX — injection order is
			// the guarantee (RFC-0030 §1).
			Env: []apiv1.EnvVar{{Name: "FISSION_STATE_URL", Value: "http://evil"}},
		}}
		ApplyFunctionEnv(spec, "runtime", fn, platform)

		runtime := spec.Containers[0]
		last := runtime.Env[len(runtime.Env)-1]
		assert.Equal(t, "FISSION_STATE_URL", last.Name)
		assert.Equal(t, "http://statesvc", last.Value, "the platform value must be the winning (last) entry")
	})

	t.Run("missing container name is a no-op", func(t *testing.T) {
		t.Parallel()
		spec, want := basePodSpec(), basePodSpec()
		fn := &fv1.Function{Spec: fv1.FunctionSpec{Env: []apiv1.EnvVar{{Name: "A", Value: "1"}}}}
		ApplyFunctionEnv(spec, "no-such-container", fn, platform)
		assert.Equal(t, want, spec)
	})
}
