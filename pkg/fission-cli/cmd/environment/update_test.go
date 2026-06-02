// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func baseEnv() *fv1.Environment {
	return &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "default"},
		Spec: fv1.EnvironmentSpec{
			Version: 2,
			Runtime: fv1.Runtime{Image: "old-image", Container: &v1.Container{Name: "e"}},
		},
	}
}

func TestUpdateExistingEnvironmentWithCmd(t *testing.T) {
	t.Parallel()

	t.Run("updates scalar fields", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.Set(flagkey.EnvImage, "new-image")
		in.Set(flagkey.EnvPoolsize, 5)
		in.Set(flagkey.EnvGracePeriod, int64(42))
		in.Set(flagkey.EnvKeeparchive, true)
		in.Set(flagkey.EnvImagePullSecret, "regcred")

		got, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.NoError(t, err)
		assert.Equal(t, "new-image", got.Spec.Runtime.Image)
		assert.Equal(t, 5, got.Spec.Poolsize)
		assert.Equal(t, int64(42), got.Spec.TerminationGracePeriod)
		assert.True(t, got.Spec.KeepArchive)
		assert.Equal(t, "regcred", got.Spec.ImagePullSecret)
	})

	t.Run("runtime env vars parsed", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.Set(flagkey.EnvRuntime, []string{"A=1", "B=2"})
		got, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.NoError(t, err)
		require.Len(t, got.Spec.Runtime.Container.Env, 2)
		assert.Equal(t, "A", got.Spec.Runtime.Container.Env[0].Name)
	})

	t.Run("cpu limit defaults to request when only min set", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.Set(flagkey.RuntimeMincpu, 100)
		got, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.NoError(t, err)
		req := got.Spec.Resources.Requests[v1.ResourceCPU]
		lim := got.Spec.Resources.Limits[v1.ResourceCPU]
		assert.Equal(t, 0, req.Cmp(lim), "limit should default to request")
	})

	t.Run("mincpu greater than maxcpu errors", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.Set(flagkey.RuntimeMincpu, 200)
		in.Set(flagkey.RuntimeMaxcpu, 100)
		_, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.Error(t, err)
	})

	t.Run("builder on a v1 environment errors", func(t *testing.T) {
		t.Parallel()
		env := baseEnv()
		env.Spec.Version = 1
		in := dummy.TestFlagSet()
		in.Set(flagkey.EnvBuilderImage, "builder:latest")
		_, err := updateExistingEnvironmentWithCmd(env, in)
		require.Error(t, err)
	})
}
