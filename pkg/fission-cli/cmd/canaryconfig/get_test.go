// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })
	os.Stdout = w
	runErr := fn()
	w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	require.NoError(t, runErr)
	return buf.String()
}

func TestCanaryGet(t *testing.T) {
	setCanaryClient(newCanary())

	t.Run("table output names the config", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.CanaryName, "canary")
		out := captureStdout(t, func() error { return Get(in) })
		assert.True(t, strings.Contains(out, "canary"), "table output should name the canary config, got: %q", out)
		assert.Contains(t, out, "NAME")
	})

	t.Run("json output is structured", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.CanaryName, "canary")
		in.Set(flagkey.Output, "json")
		out := captureStdout(t, func() error { return Get(in) })
		assert.Contains(t, out, "\"name\": \"canary\"")
	})

	t.Run("missing config errors", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.CanaryName, "absent")
		require.Error(t, Get(in))
	})
}
