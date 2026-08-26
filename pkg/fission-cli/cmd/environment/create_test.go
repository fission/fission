// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/flag"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func TestEnvRuntimeClassUsageNamesContainerExclusion(t *testing.T) {
	t.Parallel()
	assert.Contains(t, flag.EnvRuntimeClass.Usage, "does not apply to container-image functions",
		"the usage string is the only place a CLI user reads the container-executor exclusion")
}

func TestCreateEnvironmentFromCmd(t *testing.T) {
	t.Parallel()

	t.Run("runtime class set when flag given", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetString(flagkey.EnvName, "e")
		in.SetString(flagkey.EnvImage, "image")
		in.SetString(flagkey.EnvRuntimeClass, "gvisor")

		got, err := createEnvironmentFromCmd(in)
		require.NoError(t, err)
		require.NotNil(t, got.Spec.RuntimeClassName)
		assert.Equal(t, "gvisor", *got.Spec.RuntimeClassName)
	})

	t.Run("runtime class nil when flag not given", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetString(flagkey.EnvName, "e")
		in.SetString(flagkey.EnvImage, "image")

		got, err := createEnvironmentFromCmd(in)
		require.NoError(t, err)
		assert.Nil(t, got.Spec.RuntimeClassName)
	})
}
