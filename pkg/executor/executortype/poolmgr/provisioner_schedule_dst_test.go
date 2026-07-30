// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// 02:30 EST on March 9th 2025 does not exist: America/New_York's clocks jump
// from 01:59:59 EST straight to 03:00:00 EDT that day. A window whose cron
// fires at "30 2 * * *" has no local instant to fire at, and robfig/cron's
// Next walk confirms it: the fire that would have been on the 9th is skipped
// entirely, so the next fire after March 8th's is March 10th. The window
// never opens on the 9th. This is the observed, accepted behaviour.
func TestWindowActiveAtSpringForwardMissingStart(t *testing.T) {
	tests := []struct {
		name   string
		window fv1.ProvisionedWindow
		now    time.Time
		want   bool
	}{
		{
			name: "day before, control",
			window: fv1.ProvisionedWindow{
				Name:     "w1",
				Start:    "CRON_TZ=America/New_York 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 8, 7, 30, 0, 0, time.UTC), //02:30 EST
			want: true,
		},
		{
			name: "spring forward day, 06:30 UTC",
			window: fv1.ProvisionedWindow{
				Name:     "w1",
				Start:    "CRON_TZ=America/New_York 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 9, 6, 30, 0, 0, time.UTC), //01:30 EST clocks jump at 2AM
			want: false,
		},
		{
			name: "spring forward day, 07:30 UTC",
			window: fv1.ProvisionedWindow{
				Name:     "w1",
				Start:    "CRON_TZ=America/New_York 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 9, 7, 30, 0, 0, time.UTC), //03:30 EDT
			want: false,
		},
		{
			name: "next day",
			window: fv1.ProvisionedWindow{
				Name:     "w1",
				Start:    "CRON_TZ=America/New_York 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 10, 6, 30, 0, 0, time.UTC), // 02:30 EDT
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := windowActiveAt(tt.window, tt.now)
			require.NoError(t, err)
			require.Equal(t, tt.want, got, "window=%v, now=%s", tt.window, tt.now)
		})
	}
}

// Duration is absolute elapsed time, not wall-clock span: a window opening at
// 01:00 EST on March 9th 2025 with a 3h duration closes at 05:00 EDT, not
// 04:00, because the 02:00-02:59 local hour is skipped that day. Three hours
// of elapsed time span only two wall-clock hours of coverage on the far side
// of the gap. The window stays open continuously through the gap itself —
// there is no dip in coverage at the jump.
func TestWindowActiveAtSpringForwardSpanningGap(t *testing.T) {
	tests := []struct {
		name   string
		window fv1.ProvisionedWindow
		now    time.Time
		want   bool
	}{
		{
			name: "shortly after open",
			window: fv1.ProvisionedWindow{
				Name:     "w2",
				Start:    "CRON_TZ=America/New_York 0 1 * * *",
				Duration: "3h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 9, 6, 15, 0, 0, time.UTC), //01:15 EST
			want: true,
		},
		{
			name: "right before clocks jump",
			window: fv1.ProvisionedWindow{
				Name:     "w2",
				Start:    "CRON_TZ=America/New_York 0 1 * * *",
				Duration: "3h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 9, 6, 59, 59, 0, time.UTC), //01:59 EST clocks jump at 2AM
			want: true,
		},
		{
			name: "right after clocks jump",
			window: fv1.ProvisionedWindow{
				Name:     "w2",
				Start:    "CRON_TZ=America/New_York 0 1 * * *",
				Duration: "3h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 9, 7, 1, 0, 0, time.UTC), //03:01 EDT
			want: true,
		},
		{
			name: "just before close",
			window: fv1.ProvisionedWindow{
				Name:     "w2",
				Start:    "CRON_TZ=America/New_York 0 1 * * *",
				Duration: "3h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 9, 8, 59, 59, 0, time.UTC), // 4:59:59 EDT
			want: true,
		},
		{
			name: "at close",
			window: fv1.ProvisionedWindow{
				Name:     "w2",
				Start:    "CRON_TZ=America/New_York 0 1 * * *",
				Duration: "3h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 9, 9, 0, 0, 0, time.UTC), // 05:00 EDT
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := windowActiveAt(tt.window, tt.now)
			require.NoError(t, err)
			require.Equal(t, tt.want, got, "window=%v, now=%s", tt.window, tt.now)
		})
	}
}

// 01:30 EDT/EST on November 2nd 2025 happens twice: America/New_York's clocks
// fall back from 01:59:59 EDT to 01:00:00 EST, so the cron fires once at
// 05:30 UTC (01:30 EDT, first pass) and again at 06:30 UTC (01:30 EST, second
// pass). With a 1h duration the first fire's window closes at exactly 06:30
// UTC, the same instant the second fire opens — the two windows abut with no
// gap, so the function stays continuously warm for a full 2 hours instead of
// the usual 1. This double-open is intended and accepted behaviour, reviewed
// as part of RFC-0026 PR2 Stage 2: warming for an extra hour once a year in
// one timezone is a trivial cost, not a bug to fix.
func TestWindowActiveAtFallBackRepeatedHour(t *testing.T) {
	tests := []struct {
		name   string
		window fv1.ProvisionedWindow
		now    time.Time
		want   bool
	}{
		{
			name: "shortly before open",
			window: fv1.ProvisionedWindow{
				Name:     "w3",
				Start:    "CRON_TZ=America/New_York 30 1 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 5, 29, 59, 0, time.UTC), //01:29 EDT
			want: false,
		},
		{
			name: "inside 1st hour",
			window: fv1.ProvisionedWindow{
				Name:     "w3",
				Start:    "CRON_TZ=America/New_York 30 1 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 5, 45, 0, 0, time.UTC), //01:45 EDT
			want: true,
		},
		{
			name: "at the boundary",
			window: fv1.ProvisionedWindow{
				Name:     "w3",
				Start:    "CRON_TZ=America/New_York 30 1 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 6, 30, 0, 0, time.UTC), //01:30 EST - window repeats
			want: true,
		},
		{
			name: "inside the second hour",
			window: fv1.ProvisionedWindow{
				Name:     "w3",
				Start:    "CRON_TZ=America/New_York 30 1 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 7, 0, 0, 0, time.UTC), // 2:00 EST
			want: true,
		},
		{
			name: "after both close",
			window: fv1.ProvisionedWindow{
				Name:     "w3",
				Start:    "CRON_TZ=America/New_York 30 1 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 7, 30, 0, 0, time.UTC), // 02:30 EST
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := windowActiveAt(tt.window, tt.now)
			require.NoError(t, err)
			require.Equal(t, tt.want, got, "window=%v, now=%s", tt.window, tt.now)
		})
	}
}

// Duration is absolute elapsed time, not wall-clock span, same rule as
// TestWindowActiveAtSpringForwardSpanningGap but running the opposite
// direction: a window opening at 00:00 EDT on November 2nd 2025 with a 4h
// duration closes at 03:00 EST, not 04:00, because the 01:00-01:59 local
// hour repeats that day (clocks fall back at 06:00 UTC / 2am EDT->1am EST).
// Four hours of elapsed time span only three wall-clock hours on the far
// side of the fall-back, so the window closes an hour early on the clock.
// The repeated 01:xx hour is covered continuously by this one window, same
// intended/accepted behaviour as TestWindowActiveAtFallBackRepeatedHour.
func TestWindowActiveAtFallBackSpanningExtraHour(t *testing.T) {
	tests := []struct {
		name   string
		window fv1.ProvisionedWindow
		now    time.Time
		want   bool
	}{
		{
			name: "insideWindow",
			window: fv1.ProvisionedWindow{
				Name:     "w4",
				Start:    "CRON_TZ=America/New_York 0 0 * * *",
				Duration: "4h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 5, 45, 0, 0, time.UTC), //01:45 EDT
			want: true,
		},
		{
			name: "inside window after clocks fall back",
			window: fv1.ProvisionedWindow{
				Name:     "w4",
				Start:    "CRON_TZ=America/New_York 0 0 * * *",
				Duration: "4h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 6, 15, 0, 0, time.UTC), //01:15 EST
			want: true,
		},
		{
			name: "just before close",
			window: fv1.ProvisionedWindow{
				Name:     "w4",
				Start:    "CRON_TZ=America/New_York 0 0 * * *",
				Duration: "4h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 7, 59, 50, 0, time.UTC), //02:59:59 EST
			want: true,
		},
		{
			name: "at close",
			window: fv1.ProvisionedWindow{
				Name:     "w4",
				Start:    "CRON_TZ=America/New_York 0 0 * * *",
				Duration: "4h",
				Target:   2,
			},
			now:  time.Date(2025, time.November, 2, 8, 0, 0, 0, time.UTC), // 3:00 EST
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := windowActiveAt(tt.window, tt.now)
			require.NoError(t, err)
			require.Equal(t, tt.want, got, "window=%v, now=%s", tt.window, tt.now)
		})
	}
}

// Control for the New York DST goldens above: Asia/Kolkata never observes
// DST and sits at a UTC+5:30 half-hour offset. Same window shape and same
// calendar dates as TestWindowActiveAtSpringForwardMissingStart (02:30
// daily, 1h duration, March 8-10 2025), but here every day behaves plainly —
// no skipped fire, no missing window — which is the point: it confirms the
// New York test's finding is a genuine DST effect and not an artefact of the
// fixture. The half-hour offset also shows up directly in the UTC instants
// below (the ":30" minute), catching any bug that rounds a half-hour offset
// to the nearest whole hour.
func TestWindowActiveAtNoDSTControlKolkata(t *testing.T) {
	tests := []struct {
		name   string
		window fv1.ProvisionedWindow
		now    time.Time
		want   bool
	}{
		{
			name: "before window opens",
			window: fv1.ProvisionedWindow{
				Name:     "w5",
				Start:    "CRON_TZ=Asia/Kolkata 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 8, 20, 59, 59, 0, time.UTC), //02:29:59 IST
			want: false,
		},
		{
			name: "at window open",
			window: fv1.ProvisionedWindow{
				Name:     "w5",
				Start:    "CRON_TZ=Asia/Kolkata 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 8, 21, 0, 0, 0, time.UTC), //02:30 IST march 9
			want: true,
		},
		{
			name: "mid window",
			window: fv1.ProvisionedWindow{
				Name:     "w5",
				Start:    "CRON_TZ=Asia/Kolkata 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 8, 21, 30, 0, 0, time.UTC), //03:00 IST march 9
			want: true,
		},
		{
			name: "window closes",
			window: fv1.ProvisionedWindow{
				Name:     "w5",
				Start:    "CRON_TZ=Asia/Kolkata 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 8, 22, 0, 0, 0, time.UTC), //03:30 IST march 9
			want: false,
		},
		{
			name: "next day window open",
			window: fv1.ProvisionedWindow{
				Name:     "w5",
				Start:    "CRON_TZ=Asia/Kolkata 30 2 * * *",
				Duration: "1h",
				Target:   2,
			},
			now:  time.Date(2025, time.March, 9, 21, 0, 0, 0, time.UTC), //02:30 IST march 10
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := windowActiveAt(tt.window, tt.now)
			require.NoError(t, err)
			require.Equal(t, tt.want, got, "window=%v, now=%s", tt.window, tt.now)
		})
	}
}
