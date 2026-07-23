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
			want: 3,
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
						Duration: "28h", // 28 hours
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
						Start:    "0 0 * * *",
						Duration: "10m", // 10 minutes
						Target:   0,
						Name:     "sleepWindow",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
			want: 0,
		},
		{
			name: "multiple active windows, with one Target=0",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 2,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 0 * * *",
						Duration: "10m", // 10 minutes
						Target:   0,
						Name:     "sleepWindow",
					},
					{
						Start:    "0 0 * * *",
						Duration: "5m", // 5 minutes
						Target:   1,
						Name:     "activeWindow",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
			want: 1,
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

func TestWindowActiveAt(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		window  fv1.ProvisionedWindow
		now     time.Time
		want    bool
		wantErr bool
	}{
		{
			name: "window active at start",
			window: fv1.ProvisionedWindow{
				Start:    "0 0 * * *",
				Duration: "10m", // 10 minutes
			},
			now:     time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:    true,
			wantErr: false,
		},
		{
			name: "window active in the middle",
			window: fv1.ProvisionedWindow{
				Start:    "0 0 * * *",
				Duration: "10m", // 10 minutes
			},
			now:     time.Date(2025, time.January, 1, 0, 5, 0, 0, time.UTC),
			want:    true,
			wantErr: false,
		},
		{
			name: "window not active in the end",
			window: fv1.ProvisionedWindow{
				Start:    "0 0 * * *",
				Duration: "10m", // 10 minutes
			},
			now:     time.Date(2025, time.January, 1, 0, 10, 0, 0, time.UTC),
			want:    false,
			wantErr: false,
		},
		{
			name: "window not active before start",
			window: fv1.ProvisionedWindow{
				Start:    "0 1 * * *",
				Duration: "10m", // 10 minutes
			},
			now:     time.Date(2025, time.January, 1, 0, 59, 59, 999999, time.UTC),
			want:    false,
			wantErr: false,
		},
		{
			name: "window not active",
			window: fv1.ProvisionedWindow{
				Start:    "0 1 * * *",
				Duration: "10m", // 10 minutes
			},
			now:     time.Date(2025, time.January, 1, 13, 0, 0, 0, time.UTC),
			want:    false,
			wantErr: false,
		},
		{
			name: "window active just before end",
			window: fv1.ProvisionedWindow{
				Start:    "0 0 * * *",
				Duration: "10m", // 10 minutes
			},
			now:     time.Date(2025, time.January, 1, 0, 9, 59, 999999, time.UTC),
			want:    true,
			wantErr: false,
		},
		{
			name: "dense cron	window active",
			window: fv1.ProvisionedWindow{
				Start:    "* * * * * *",
				Duration: "28h", // 28 hours
			},
			now:     time.Date(2025, time.January, 4, 0, 0, 0, 0, time.UTC),
			want:    true,
			wantErr: false,
		},

		// weekend tests

		{
			name: "window active on weekend",
			window: fv1.ProvisionedWindow{
				Start:    "0 0 * * 6", // runs at midnight on Saturday
				Duration: "48h",       // runs from saturday midnight to Monday midnight (12AM on Monday)
			},
			now:     time.Date(2025, time.January, 4, 13, 0, 0, 0, time.UTC),
			want:    true,
			wantErr: false,
		},
		{
			name: "window inactive on weekday",
			window: fv1.ProvisionedWindow{
				Start:    "0 0 * * 6", // runs at midnight on Saturday
				Duration: "48h",       // runs from saturday midnight to Monday midnight (12AM on Monday)
			},
			now:     time.Date(2025, time.January, 6, 0, 0, 0, 0, time.UTC), // Monday 12AM
			want:    false,
			wantErr: false,
		},
		{
			name: "window inactive on wednesday",
			window: fv1.ProvisionedWindow{
				Start:    "0 0 * * 6", // runs at midnight on Saturday
				Duration: "48h",       // runs from saturday midnight to Monday midnight (12AM on Monday)
			},
			now:     time.Date(2025, time.January, 8, 0, 0, 0, 0, time.UTC), // Monday 12AM
			want:    false,
			wantErr: false,
		},
		{
			name: "active specific hours, combined days: Saturday",
			window: fv1.ProvisionedWindow{
				Start:    "0 9 * * 0,6", // runs at midnight on Saturday
				Duration: "8h",          // runs from saturday midnight to Monday midnight (12AM on Monday)
			},
			now:     time.Date(2025, time.January, 4, 10, 0, 0, 0, time.UTC),
			want:    true,
			wantErr: false,
		},
		{
			name: "inactive specific hours, combined days: Sunday",
			window: fv1.ProvisionedWindow{
				Start:    "0 9 * * 0,6", // runs at 9AM on Saturday and Sunday
				Duration: "8h",          // runs 8 hours 9AM to 5PM on Saturday and Sunday
			},
			now:     time.Date(2025, time.January, 5, 18, 0, 0, 0, time.UTC),
			want:    false,
			wantErr: false,
		},
		{
			name: "inactive specific hours, combined days: Saturday",
			window: fv1.ProvisionedWindow{
				Start:    "0 9 * * 0,6", // runs at 9AM on Saturday and Sunday
				Duration: "8h",          // runs 8 hours 9AM to 5PM on Saturday and Sunday
			},
			now:     time.Date(2025, time.January, 4, 18, 0, 0, 0, time.UTC),
			want:    false,
			wantErr: false,
		},

		// errors

		{
			name: "incorrect cron syntax throws error",
			window: fv1.ProvisionedWindow{
				Start:    "incorrect",
				Duration: "8h",
			},
			now:     time.Date(2025, time.January, 4, 18, 0, 0, 0, time.UTC),
			want:    false,
			wantErr: true,
		},
		{
			name: "incorrect Duration throws error",
			window: fv1.ProvisionedWindow{
				Start:    "0 9 * * 0,6", // runs at 9AM on Saturday and Sunday
				Duration: "incorrect",
			},
			now:     time.Date(2025, time.January, 4, 18, 0, 0, 0, time.UTC),
			want:    false,
			wantErr: true,
		},
		{
			name: "zero Duration throws error",
			window: fv1.ProvisionedWindow{
				Start:    "0 9 * * 0,6", // runs at 9AM on Saturday and Sunday
				Duration: "0s",
			},
			now:     time.Date(2025, time.January, 4, 18, 0, 0, 0, time.UTC),
			want:    false,
			wantErr: true,
		},
		{
			name: "negative Duration throws error",
			window: fv1.ProvisionedWindow{
				Start:    "0 9 * * 0,6", // runs at 9AM on Saturday and Sunday
				Duration: "-1s",
			},
			now:     time.Date(2025, time.January, 4, 18, 0, 0, 0, time.UTC),
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := windowActiveAt(tt.window, tt.now)
			if tt.wantErr {
				require.Error(t, gotErr)
			} else {
				require.NoError(t, gotErr)
			}
			if got != tt.want {
				t.Errorf("windowActiveAt(%+v,%v) = %v, want %v", tt.window, tt.now, got, tt.want)
			}
		})
	}
}

func TestNextTransitionAt(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		cfg           *fv1.ProvisionedConcurrencyConfig
		now           time.Time
		want          time.Time
		wantErrSubStr []string
	}{
		{
			name: "single inactive window returns next start time",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * *", // runs at 1AM
						Duration: "30m",       // 30 minutes
						Name:     "window1",
					},
				},
			},
			now:           time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:          time.Date(2025, time.January, 1, 1, 0, 0, 0, time.UTC),
			wantErrSubStr: []string{},
		},
		{
			name: "single active window returns next stop time",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * *", // runs at 1AM
						Duration: "60m",       // 60 minutes
						Name:     "window1",
					},
				},
			},
			now:           time.Date(2025, time.January, 1, 1, 10, 0, 0, time.UTC),
			want:          time.Date(2025, time.January, 1, 2, 0, 0, 0, time.UTC),
			wantErrSubStr: []string{},
		},
		{
			name: "multiple windows, one active, stopping soon, other inactive starting sooner",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * *", // runs at 1AM
						Duration: "60m",       // 30 minutes
						Name:     "window1",
					},
					{
						Start:    "30 1 * * *", // runs at 1:30AM
						Duration: "60m",        // 60 minutes
						Name:     "window2",
					},
				},
			},
			now:           time.Date(2025, time.January, 1, 1, 10, 0, 0, time.UTC),
			want:          time.Date(2025, time.January, 1, 1, 30, 0, 0, time.UTC),
			wantErrSubStr: []string{},
		},
		{
			name: "multiple windows, one active, stopping sooner, other inactive starting soon",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * *", // runs at 1AM
						Duration: "15m",       // 15 minutes
						Name:     "window1",
					},
					{
						Start:    "30 1 * * *", // runs at 1:30AM
						Duration: "60m",        // 60 minutes
						Name:     "window2",
					},
				},
			},
			now:           time.Date(2025, time.January, 1, 1, 10, 0, 0, time.UTC),
			want:          time.Date(2025, time.January, 1, 1, 15, 0, 0, time.UTC),
			wantErrSubStr: []string{},
		},
		{
			name: "same window dense-cron edge case",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "* * * * *", // runs every minute
						Duration: "5m",        // 5 minutes
						Name:     "window1",
					},
				},
			},
			now:           time.Date(2025, time.January, 1, 0, 2, 0, 0, time.UTC),
			want:          time.Date(2025, time.January, 1, 0, 7, 0, 0, time.UTC),
			wantErrSubStr: []string{},
		},
		{
			name: "iteration cap",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "* * * * * *",
						Duration: "28h",
						Name:     "window1",
					},
				},
			},
			now:           time.Date(2025, time.January, 1, 0, 2, 0, 0, time.UTC),
			want:          time.Date(2025, time.January, 1, 0, 2, 0, 0, time.UTC),
			wantErrSubStr: []string{},
		},
		{
			name: "empty cfg.window",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target:  4,
				Windows: []fv1.ProvisionedWindow{},
			},
			now:           time.Date(2025, time.January, 1, 0, 2, 0, 0, time.UTC),
			want:          time.Time{},
			wantErrSubStr: []string{},
		},
		{
			name: "one bad window, one good window",
			cfg: &fv1.ProvisionedConcurrencyConfig{
				Target: 4,
				Windows: []fv1.ProvisionedWindow{
					{
						Start:    "0 1 * * * ",
						Duration: "10m",
						Name:     "window1",
					},
					{
						Start:    "01***",
						Duration: "10m",
						Name:     "window2",
					},
				},
			},
			now:  time.Date(2025, time.January, 1, 0, 2, 0, 0, time.UTC),
			want: time.Date(2025, time.January, 1, 1, 0, 0, 0, time.UTC),
			wantErrSubStr: []string{
				"window window2 failed",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := nextTransitionAt(tt.cfg, tt.now)
			require.Len(t, gotErr, len(tt.wantErrSubStr))
			if len(tt.wantErrSubStr) > 0 {
				for i, substr := range tt.wantErrSubStr {
					require.Contains(t, gotErr[i].Error(), substr)
				}
			}
			if !got.Equal(tt.want) {
				t.Errorf("nextTransitionAt(%+v,%v) = %v, want %v", tt.cfg, tt.now, got, tt.want)
			}
		})
	}
}
