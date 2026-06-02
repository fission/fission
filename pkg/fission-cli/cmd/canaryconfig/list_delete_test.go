// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func TestCanaryList(t *testing.T) {
	setCanaryClient(newCanary())

	t.Run("table lists the config", func(t *testing.T) {
		out := captureStdout(t, func() error { return List(dummy.TestFlagSet()) })
		assert.Contains(t, out, "NAME")
		assert.Contains(t, out, "canary")
	})

	t.Run("json output", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.Output, "json")
		out := captureStdout(t, func() error { return List(in) })
		assert.Contains(t, out, "canary")
	})

	t.Run("invalid format errors", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.Output, "bogus")
		require.Error(t, List(in))
	})
}

func TestCanaryDelete(t *testing.T) {
	setCanaryClient(newCanary())

	t.Run("deletes an existing config", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.CanaryName, "canary")
		_ = captureStdout(t, func() error { return Delete(in) })
	})

	t.Run("deleting a missing config errors", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.CanaryName, "canary") // already deleted above
		require.Error(t, Delete(in))
	})

	t.Run("ignore-not-found swallows the error", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.CanaryName, "canary")
		in.Set(flagkey.IgnoreNotFound, true)
		require.NoError(t, Delete(in))
	})
}
