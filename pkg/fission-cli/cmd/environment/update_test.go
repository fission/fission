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
		in.SetString(flagkey.EnvImage, "new-image")
		in.SetInt(flagkey.EnvPoolsize, 5)
		in.SetInt64(flagkey.EnvGracePeriod, int64(42))
		in.SetBool(flagkey.EnvKeeparchive, true)
		in.SetString(flagkey.EnvImagePullSecret, "regcred")

		got, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.NoError(t, err)
		assert.Equal(t, "new-image", got.Spec.Runtime.Image)
		assert.Equal(t, 5, got.Spec.Poolsize)
		require.NotNil(t, got.Spec.TerminationGracePeriod)
		assert.Equal(t, int64(42), *got.Spec.TerminationGracePeriod)
		assert.True(t, got.Spec.KeepArchive)
		assert.Equal(t, "regcred", got.Spec.ImagePullSecret)
	})

	t.Run("explicit zero grace period is preserved, not dropped to the default", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetInt64(flagkey.EnvGracePeriod, int64(0))

		got, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.NoError(t, err)
		// The pointer field is the whole mechanism: a non-nil 0 marshals as
		// `"terminationGracePeriod":0` and the apiserver keeps it, where the
		// previous int64+omitempty dropped it and served back 90.
		require.NotNil(t, got.Spec.TerminationGracePeriod)
		assert.Zero(t, *got.Spec.TerminationGracePeriod)
	})

	t.Run("grace period untouched when the flag is not set", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetString(flagkey.EnvImage, "new-image")

		got, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.NoError(t, err)
		assert.Nil(t, got.Spec.TerminationGracePeriod,
			"an unrelated update must not materialize a grace value the user never set")
	})

	t.Run("runtime env vars parsed", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetStringSlice(flagkey.EnvRuntime, []string{"A=1", "B=2"})
		got, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.NoError(t, err)
		require.Len(t, got.Spec.Runtime.Container.Env, 2)
		assert.Equal(t, "A", got.Spec.Runtime.Container.Env[0].Name)
	})

	t.Run("cpu limit defaults to request when only min set", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetInt(flagkey.RuntimeMincpu, 100)
		got, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.NoError(t, err)
		req := got.Spec.Resources.Requests[v1.ResourceCPU]
		lim := got.Spec.Resources.Limits[v1.ResourceCPU]
		assert.Equal(t, 0, req.Cmp(lim), "limit should default to request")
	})

	t.Run("mincpu greater than maxcpu errors", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetInt(flagkey.RuntimeMincpu, 200)
		in.SetInt(flagkey.RuntimeMaxcpu, 100)
		_, err := updateExistingEnvironmentWithCmd(baseEnv(), in)
		require.Error(t, err)
	})

	t.Run("builder on a v1 environment errors", func(t *testing.T) {
		t.Parallel()
		env := baseEnv()
		env.Spec.Version = 1
		in := dummy.TestFlagSet()
		in.SetString(flagkey.EnvBuilderImage, "builder:latest")
		_, err := updateExistingEnvironmentWithCmd(env, in)
		require.Error(t, err)
	})
}
