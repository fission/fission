// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cobra

import (
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/fission-cli/flag"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// TestFlagDefaultsReachCobra pins that every default declared on a flag.Flag
// is the default the cobra flag set reports for its kind. Before the typed
// default fields, an Int64 flag declared with an untyped 360 was silently
// registered with default 0 (the int did not assert to int64), which is how
// `fission env create` without --graceperiod produced instant-kill
// environments once the spec field became a pointer.
func TestFlagDefaultsReachCobra(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		f    flag.Flag
		want string // cobra's rendered default
	}{
		{name: "int64 grace period", f: flag.EnvTerminationGracePeriod, want: "90"},
		{name: "int64 function grace period", f: flag.FnTerminationGracePeriod, want: "360"},
		{name: "int verbosity", f: flag.GlobalVerbosity, want: "1"},
		{name: "string executor type", f: flag.FnExecutorType, want: "poolmgr"},
		{name: "duration test timeout", f: flag.FnTestTimeout, want: (60 * time.Second).String()},
		{name: "bool default false", f: flag.Flag{Type: flag.Bool, Name: "b"}, want: "false"},
		{name: "bool default true", f: flag.Flag{Type: flag.Bool, Name: "b", DefaultBool: true}, want: "true"},
		{name: "string slice", f: flag.Flag{Type: flag.StringSlice, Name: "s", DefaultStrings: []string{"a", "b"}}, want: "[a,b]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{Use: "x"}
			toCobraFlag(cmd, tt.f, false)
			got := cmd.Flags().Lookup(tt.f.Name)
			require.NotNil(t, got, "flag %q must be registered", tt.f.Name)
			assert.Equal(t, tt.want, got.DefValue)
		})
	}

	// The env grace-period flag in particular must not default to an explicit
	// zero: create hands the value through as a pointer, so 0 is instant kill.
	cmd := &cobra.Command{Use: "env"}
	toCobraFlag(cmd, flag.EnvTerminationGracePeriod, false)
	v, err := cmd.Flags().GetInt64(flagkey.EnvGracePeriod)
	require.NoError(t, err)
	assert.EqualValues(t, 90, v)
}
