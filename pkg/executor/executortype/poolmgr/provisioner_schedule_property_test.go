// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// referenceInstant anchors every generator and property in this file to one
// fixed point in time (rather than time.Now()), so a shrunk failing case
// reproduces identically on every future run.
var referenceInstant = time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

// newScheduleCronParser returns a parser using the exact option set
// windowActiveAt/effectiveTargetAt use in production, so parsing here can
// never accept or reject something differently than production would.
func newScheduleCronParser() cron.Parser {
	return cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

// genWindow draws one random, always-valid ProvisionedWindow. Cron fields
// are drawn from small fixed grammars rather than free text, so the budget
// is spent on window-arithmetic edge cases instead of bouncing off parse
// errors (see plan Stage 1, section 1a).
func genWindow(t *rapid.T, namePrefix string) fv1.ProvisionedWindow {
	minute := rapid.SampledFrom([]string{"0", "15", "30", "45", "*"}).Draw(t, "minute")
	hour := rapid.SampledFrom([]string{"0", "9", "13", "23", "*"}).Draw(t, "hour")
	dow := rapid.SampledFrom([]string{"*", "6", "0,6", "1-5"}).Draw(t, "dow")
	cronBody := minute + " " + hour + " * * " + dow
	tzPrefix := rapid.SampledFrom([]string{"", "CRON_TZ=UTC", "CRON_TZ=America/New_York", "CRON_TZ=Asia/Kolkata"}).Draw(t, "tzPrefix")
	cronString := ""
	if tzPrefix == "" {
		cronString = cronBody
	} else {
		cronString = tzPrefix + " " + cronBody
	}
	duration := rapid.SampledFrom([]string{"1m", "5m", "15m", "1h", "3h", "6h"}).Draw(t, "duration")
	target := rapid.IntRange(0, 10).Draw(t, "target")
	id := uuid.New().String()
	short := id[:6]
	name := fmt.Sprintf("%s-%s", namePrefix, short)
	return fv1.ProvisionedWindow{
		Name:     name,
		Start:    cronString,
		Duration: duration,
		Target:   target,
	}
}

// drawWindows draws between 0 and max random windows. 0 must stay reachable
// (rather than always drawing max) so properties also exercise the plain
// "no windows at all" shape of a config.
func drawWindows(t *rapid.T, namePrefix string, max int) []fv1.ProvisionedWindow {
	windows := []fv1.ProvisionedWindow{}
	n := rapid.IntRange(0, max).Draw(t, "numWindows")
	for range n {
		windows = append(windows, genWindow(t, namePrefix))
	}
	return windows
}

// drawUniformInstant picks an instant uniformly across the month following
// referenceInstant, with no bias toward any particular window's fire.
func drawUniformInstant(t *rapid.T) time.Time {
	offsetMinutes := rapid.IntRange(0, 60*24*30).Draw(t, "uniformOffsetMinutes")
	return referenceInstant.Add(time.Duration(offsetMinutes) * time.Minute)
}

// drawInstantNearFire mixes two instant strategies for a given window:
//   - edge-biased: exactly on one of the five points that define its
//     half-open interval (fire, fire+1ns, close-1ns, close, close+1ns) —
//     these are where off-by-one boundary bugs live, and uniform random
//     instants would essentially never land on them by chance.
//   - uniform: any instant in the month following referenceInstant, to
//     also catch broad, non-boundary bugs.
//
// See plan Stage 1, property 1, for the concrete bug class each half catches.
func drawInstantNearFire(t *rapid.T, window fv1.ProvisionedWindow) time.Time {
	sched, err := newScheduleCronParser().Parse(window.Start)
	require.NoError(t, err)
	fireTime := sched.Next(referenceInstant)
	dur, err := time.ParseDuration(window.Duration)
	require.NoError(t, err)
	require.Greater(t, dur, time.Duration(0))

	if rapid.Bool().Draw(t, "edgeBiased") {
		offset := rapid.SampledFrom(
			[]time.Duration{
				0,
				time.Nanosecond,
				dur - time.Nanosecond,
				dur + time.Nanosecond,
				dur,
			},
		).Draw(t, "edgeOffset")
		return fireTime.Add(offset)
	}
	return drawUniformInstant(t)
}

// drawHostileOrWideInstant mixes a handful of deliberately hostile instants
// (zero time, Unix epoch, centuries away in either direction) with a
// genuinely wide random range, so a totality property exercises both known
// nasty edges and arbitrary instants nobody thought to hand-pick.
func drawHostileOrWideInstant(t *rapid.T) time.Time {
	instant := rapid.SampledFrom([]time.Time{
		{},
		time.Unix(0, 0),
		time.Date(2225, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1824, time.January, 1, 0, 0, 0, 0, time.UTC),
		referenceInstant.Add(time.Hour * 2310),
		referenceInstant.Add(time.Hour * -2310),
	}).Draw(t, "instant")
	if rapid.Bool().Draw(t, "hostile") {
		return instant
	}
	offsetMinutes := rapid.IntRange(-60*24*30*12*100, 60*24*30*12*100).Draw(t, "wideRange")
	return referenceInstant.Add(time.Duration(offsetMinutes) * time.Minute)
}

func TestGenWindowProducesParseableWindows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		window := genWindow(t, "smoke")
		require.NoError(t, fv1.IsValidCronSpec(window.Start))
		dur, err := time.ParseDuration(window.Duration)
		require.NoError(t, err)
		require.Greater(t, dur, time.Duration(0))
		require.GreaterOrEqual(t, window.Target, 0)
	})
}

// windowActiveAtOracle is windowActiveAt's oracle (RFC-0026 PR2 missing-
// invariants plan, Stage 1, 1b): a deliberately different algorithm that
// collects every fire in [now-duration, now] and does a plain membership
// check, instead of production's single-fire early-exit walk. Agreement
// between the two across hundreds of random cases is what gives property 1
// its power; a shared bug would not be caught this way.
func windowActiveAtOracle(window fv1.ProvisionedWindow, now time.Time) (bool, error) {
	sched, err := newScheduleCronParser().Parse(window.Start)
	if err != nil {
		return false, err
	}
	dur, err := time.ParseDuration(window.Duration)
	if err != nil {
		return false, err
	}
	if dur <= 0 {
		return false, fmt.Errorf("window duration must be positive")
	}

	fires := []time.Time{}
	cursor := now.Add(-dur)
	for range 10000 {
		next := sched.Next(cursor)
		if next.IsZero() || next.After(now) {
			break
		}
		fires = append(fires, next)
		cursor = next
	}

	for _, fire := range fires {
		if !fire.After(now) && now.Before(fire.Add(dur)) {
			return true, nil
		}
	}
	return false, nil
}

// TestWindowActiveAtOracle sanity-checks the oracle itself against the same
// table windowActiveAt's own example test uses, before the oracle is trusted
// as a comparison point in any property.
func TestWindowActiveAtOracle(t *testing.T) {
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
			got, gotErr := windowActiveAtOracle(tt.window, tt.now)
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

// TestWindowActiveAtMatchesOracle is property 1 (RFC-0026 PR2 missing-
// invariants plan, Stage 1): for any legal window and any instant, production
// and the oracle must agree on whether the window is open.
func TestWindowActiveAtMatchesOracle(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		window := genWindow(t, "p1")
		now := drawInstantNearFire(t, window)
		gotOracle, oracleErr := windowActiveAtOracle(window, now)
		gotProd, prodErr := windowActiveAt(window, now)
		require.NoError(t, oracleErr)
		require.NoError(t, prodErr)
		require.Equal(t, gotOracle, gotProd, "window=%+v now=%s", window, now.Format(time.RFC3339Nano))
	})
}

// TestEffectiveTargetAtMatchesMaxOpenWindowTarget is property 3 (Stage 1):
// the effective target is the base target when no window is open, or the
// largest target among the currently-open windows otherwise — including the
// case where an open Target:0 window must beat a larger base target, not the
// other way around.
func TestEffectiveTargetAtMatchesMaxOpenWindowTarget(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		windows := drawWindows(t, "p3", 4)
		baseTarget := rapid.IntRange(1, 10).Draw(t, "baseTarget")
		pcConfig := fv1.ProvisionedConcurrencyConfig{
			Target:  baseTarget,
			Windows: windows,
		}
		var now time.Time
		if len(windows) > 0 {
			index := rapid.IntRange(0, len(windows)-1).Draw(t, "index")
			now = drawInstantNearFire(t, windows[index])
		} else {
			now = drawUniformInstant(t)
		}
		openSet := []fv1.ProvisionedWindow{}
		for _, window := range windows {
			if open, _ := windowActiveAtOracle(window, now); open {
				openSet = append(openSet, window)
			}
		}
		expected := 0
		if len(openSet) == 0 {
			expected = baseTarget
		} else {
			for _, w := range openSet {
				if w.Target > expected {
					expected = w.Target
				}
			}
		}
		gotTarget, errs := effectiveTargetAt(&pcConfig, now)
		require.Equal(t, 0, len(errs))
		require.Equal(t, expected, gotTarget, "cfg=%+v now=%s openSet=%v", pcConfig, now, openSet)
	})
}

// TestScheduleEvaluationIsTotal is property 2 (Stage 1): windowActiveAt and
// effectiveTargetAt must return (never panic, never hang) for any instant,
// however hostile, and effectiveTargetAt's result must never be negative.
func TestScheduleEvaluationIsTotal(t *testing.T) {
	// windowActiveAt is total
	rapid.Check(t, func(t *rapid.T) {
		window := genWindow(t, "p2")
		now := drawHostileOrWideInstant(t)
		_, err := windowActiveAt(window, now)
		require.NoError(t, err)
	})
	// effectiveTargetAt is total
	rapid.Check(t, func(t *rapid.T) {
		windows := drawWindows(t, "p3", 4)
		target := rapid.IntRange(1, 10).Draw(t, "target")
		pcConfig := fv1.ProvisionedConcurrencyConfig{
			Target:  target,
			Windows: windows,
		}
		now := drawHostileOrWideInstant(t)
		gotTarget, errs := effectiveTargetAt(&pcConfig, now)
		require.Equal(t, 0, len(errs))
		require.GreaterOrEqual(t, gotTarget, 0)
	})
}

func TestEffectiveTargetAtIsOrderAndDuplicateInvariant(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		windows := drawWindows(t, "p4", 4)
		baseTarget := rapid.IntRange(0, 10).Draw(t, "baseTarget")
		var instant time.Time
		if len(windows) > 0 {
			index := rapid.IntRange(0, len(windows)-1).Draw(t, "index")
			instant = drawInstantNearFire(t, windows[index])
		} else {
			instant = drawUniformInstant(t)
		}
		cfgOriginal := fv1.ProvisionedConcurrencyConfig{
			Target:  baseTarget,
			Windows: windows,
		}
		originalTarget, errs := effectiveTargetAt(&cfgOriginal, instant)
		require.Equal(t, 0, len(errs))
		if len(windows) == 0 {
			return
		}

		// shuffle windows
		shuffledWindows := rapid.Permutation(windows).Draw(t, "shuffledWindows")
		cfgShuffled := fv1.ProvisionedConcurrencyConfig{
			Target:  baseTarget,
			Windows: shuffledWindows,
		}
		shuffledTarget, errs := effectiveTargetAt(&cfgShuffled, instant)
		require.Equal(t, 0, len(errs))

		require.Equal(t, originalTarget, shuffledTarget)

		dupIndex := rapid.IntRange(0, len(windows)-1).Draw(t, "dupIndex")
		windows = append(windows, windows[dupIndex])

		cfgDupe := fv1.ProvisionedConcurrencyConfig{
			Target:  baseTarget,
			Windows: windows,
		}
		dupTarget, errs := effectiveTargetAt(&cfgDupe, instant)

		require.Equal(t, 0, len(errs))

		require.Equal(t, originalTarget, dupTarget)
	})
}
