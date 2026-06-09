// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// fakeStreamInput embeds cli.Input (nil) and overrides only the accessors
// getStreamingConfig uses.
type fakeStreamInput struct {
	cli.Input
	b map[string]bool
	s map[string]string
	i map[string]int
}

func (f fakeStreamInput) Bool(k string) bool     { return f.b[k] }
func (f fakeStreamInput) String(k string) string { return f.s[k] }
func (f fakeStreamInput) Int(k string) int       { return f.i[k] }

func TestGetStreamingConfig(t *testing.T) {
	t.Parallel()

	t.Run("streaming off yields nil", func(t *testing.T) {
		t.Parallel()
		in := fakeStreamInput{b: map[string]bool{flagkey.FnStreaming: false}}
		assert.Nil(t, getStreamingConfig(in))
	})

	t.Run("streaming on with defaults", func(t *testing.T) {
		t.Parallel()
		in := fakeStreamInput{
			b: map[string]bool{flagkey.FnStreaming: true},
			s: map[string]string{flagkey.FnStreamingProtocol: "auto"},
			i: map[string]int{flagkey.FnStreamingIdleTimeout: 60, flagkey.FnStreamingMaxDuration: 0},
		}
		sc := getStreamingConfig(in)
		require.NotNil(t, sc)
		assert.True(t, sc.Enabled)
		assert.Equal(t, fv1.StreamingAuto, sc.Protocol)
		assert.Equal(t, 60, sc.IdleTimeoutSeconds)
		assert.Equal(t, 0, sc.MaxDurationSeconds)
	})

	t.Run("streaming on with overrides", func(t *testing.T) {
		t.Parallel()
		in := fakeStreamInput{
			b: map[string]bool{flagkey.FnStreaming: true},
			s: map[string]string{flagkey.FnStreamingProtocol: "websocket"},
			i: map[string]int{flagkey.FnStreamingIdleTimeout: 15, flagkey.FnStreamingMaxDuration: 600},
		}
		sc := getStreamingConfig(in)
		require.NotNil(t, sc)
		assert.Equal(t, fv1.StreamingWebSocket, sc.Protocol)
		assert.Equal(t, 15, sc.IdleTimeoutSeconds)
		assert.Equal(t, 600, sc.MaxDurationSeconds)
	})
}
