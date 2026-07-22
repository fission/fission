// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestEffectiveTargetAt(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		cfg                  *fv1.ProvisionedConcurrencyConfig
		now                  time.Time
		want                 int
		wantBadWindows       int
		wantBadWindowsSubstr []string
	}{
		{
			name: "Return Target, no windows",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target:  4,
				Windows: []fv1.ProvisionedWindow{},
			},
			now:  time.Now(),
			want: 4,
		},
		{
			name: "Return target: inactive Window",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 0 * * *", // runs at midnight
						Duration: "30m",       // 30 minutes
						Target:   5,
						Name:     "midnight5",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 1, 0, 0, 0, time.UTC),
			want: 4,
		},
		{
			name: "Return target: active Window",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 0 * * *", // runs at midnight
						Duration: "30m",       // 30 minutes
						Target:   3,
						Name:     "midnight5",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
			want: 4,
		},
		{
			name: "Return windowTarget: active Window",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 0 * * *", // runs at midnight
						Duration: "30m",       // 30 minutes
						Target:   5,
						Name:     "midnight5",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
			want: 5,
		},
		{
			name: "Returns Correct Target: 3 windows",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * *", // runs at 1AM
						Duration: "30m",       // 30 minutes
						Target:   5,
						Name:     "midnight5",
					},
					{
						Start:    "0 0 * * *", // runs at midnight
						Duration: "30m",       // 30 minutes
						Target:   6,
						Name:     "midnight6",
					},
					{
						Start:    "0 2 * * *", // runs at 2AM
						Duration: "30m",       // 30 minutes
						Target:   7,
						Name:     "midnight7",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
			want: 6,
		},
		{
			name: "returns active window target when Target=0",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 0,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * *", // runs at 1AM
						Duration: "30m",       // 30 minutes
						Target:   5,
						Name:     "midnight5",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 1, 0, 0, 0, time.UTC),
			want: 5,
		},
		{
			name: "bad window does not effect target",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 0,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * *", // runs at 1AM
						Duration: "30m",       // 30 minutes
						Target:   5,
						Name:     "midnight5",
					},
					{
						Start:    "01***", // bad window
						Duration: "30m",   // 30 minutes
						Target:   5,
						Name:     "midnighterror",
					},
				},
			},
			now:                  time.Date(2025, time.January, 1, 1, 0, 0, 0, time.UTC),
			want:                 5,
			wantBadWindows:       1,
			wantBadWindowsSubstr: []string{"window midnighterror failed"},
		},
		{
			name: "multiple active windows return the largest target",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 2,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * *", // runs at 1AM
						Duration: "60m",       // 60 minutes
						Target:   5,
						Name:     "midnight5",
					},
					{
						Start:    "15 1 * * *", // runs at 1:15 AM
						Duration: "30m",        // 30 minutes
						Target:   6,
						Name:     "midnight6",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 1, 30, 0, 0, time.UTC),
			want: 6,
		},
		{
			name: "dense cron returns true",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 2,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "* * * * * *",
						Duration: "28h", // 60 minutes
						Target:   5,
						Name:     "denseWindow",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 1, 30, 0, 0, time.UTC),
			want: 5,
		},
		{
			name: "returns active window target when Target is 0",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 2,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "* 0 * * *",
						Duration: "10m", // 60 minutes
						Target:   0,
						Name:     "sleepWindow",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErrors := effectiveTargetAt(tt.cfg, tt.now)
			require.Len(t, gotErrors, tt.wantBadWindows)
			for i, err := range gotErrors {
				require.Contains(t, err.Error(), tt.wantBadWindowsSubstr[i])
			}
			if got != tt.want {
				t.Errorf("effectiveTargetAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_windowActiveAt(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		window  fv1.ProvisionedWindow
		now     time.Time
		want    bool
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := windowActiveAt(tt.window, tt.now)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("windowActiveAt() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("windowActiveAt() succeeded unexpectedly")
			}
			// TODO: update the condition below to compare got with tt.want.
			if true {
				t.Errorf("windowActiveAt() = %v, want %v", got, tt.want)
			}
		})
	}
}
