// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package flag

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckDefault pins the Type↔Default* pairing. The compiler cannot tie the
// discriminator to its variants, so a default in the wrong field would build
// and then be dropped in silence — the same shape as the untyped DefaultValue
// that made both --graceperiod flags register as 0.
func TestCheckDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		flag    Flag
		wantErr string // substring; empty means valid
	}{
		{name: "no default at all", flag: Flag{Type: Int64, Name: "a"}},
		{name: "bool default", flag: Flag{Type: Bool, Name: "a", DefaultBool: true}},
		{name: "string default", flag: Flag{Type: String, Name: "a", DefaultString: "x"}},
		{name: "string slice default", flag: Flag{Type: StringSlice, Name: "a", DefaultStrings: []string{"x"}}},
		{name: "int default", flag: Flag{Type: Int, Name: "a", DefaultInt: 1}},
		{name: "int64 default", flag: Flag{Type: Int64, Name: "a", DefaultInt64: 90}},
		{name: "duration default", flag: Flag{Type: Duration, Name: "a", DefaultDuration: time.Second}},

		// The regression this guard exists for: an integer default on an Int64
		// flag. Before the typed fields it was an untyped literal in an `any`;
		// now it is DefaultInt, and either way the flag would register as 0.
		{
			name:    "int default on an int64 flag",
			flag:    Flag{Type: Int64, Name: "graceperiod", DefaultInt: 90},
			wantErr: `flag "graceperiod": this kind reads DefaultInt64, but DefaultInt is set`,
		},
		{
			name:    "int default on a duration flag",
			flag:    Flag{Type: Duration, Name: "timeout", DefaultInt: 30},
			wantErr: "this kind reads DefaultDuration, but DefaultInt is set",
		},
		{
			name:    "two defaults set",
			flag:    Flag{Type: Int, Name: "a", DefaultInt: 1, DefaultString: "x"},
			wantErr: "DefaultString, DefaultInt is set",
		},
		{
			name:    "default on a kind that takes none",
			flag:    Flag{Type: Float64, Name: "a", DefaultInt: 1},
			wantErr: "this kind takes no default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.flag.CheckDefault()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestDeclaredFlagsPairTypeAndDefault walks the flag sets the CLI actually
// declares. TestCommandTreeLeavesAreRunnable covers the ones reachable from a
// command; this covers the declarations directly, so a flag added but not yet
// wired is still checked.
func TestDeclaredFlagsPairTypeAndDefault(t *testing.T) {
	t.Parallel()
	for _, f := range []Flag{
		GlobalVerbosity, EnvTerminationGracePeriod, FnTerminationGracePeriod,
		FnSpecializationTimeout, FnPort, FnExecutorType, FnTestTimeout,
		WaitTimeout, PkgWatchTimeout, SupportOutput,
	} {
		assert.NoErrorf(t, f.CheckDefault(), "flag %q", f.Name)
	}
}
