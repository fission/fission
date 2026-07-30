// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"fmt"
	"slices"
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

func genDenseWindow(t *rapid.T, namePrefix string) fv1.ProvisionedWindow {
	second := rapid.SampledFrom([]string{"*", "*/5", "*/10"}).Draw(t, "second")
	minute := "*"
	hour := "*"
	dow := "*"
	cronString := second + " " + minute + " " + hour + " * * " + dow
	duration := rapid.SampledFrom([]string{"300h", "500h", "800h", "1000h"}).Draw(t, "duration")
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

// genWindow draws one random, always-valid ProvisionedWindow. Cron fields
// are drawn from small fixed grammars rather than free text, so the budget
// is spent on window-arithmetic edge cases instead of bouncing off parse
// errors (see plan Stage 1, section 1a).
func genWindow(t *rapid.T, namePrefix string, forceTZ bool) fv1.ProvisionedWindow {
	minute := rapid.SampledFrom([]string{"0", "15", "30", "45", "*"}).Draw(t, "minute")
	hour := rapid.SampledFrom([]string{"0", "9", "13", "23", "*"}).Draw(t, "hour")
	dow := rapid.SampledFrom([]string{"*", "6", "0,6", "1-5"}).Draw(t, "dow")
	cronBody := minute + " " + hour + " * * " + dow
	tzPrefix := ""
	if forceTZ {
		tzPrefix = rapid.SampledFrom([]string{"CRON_TZ=UTC", "CRON_TZ=America/New_York", "CRON_TZ=Asia/Kolkata"}).Draw(t, "tzPrefix")
	} else {
		tzPrefix = rapid.SampledFrom([]string{"", "CRON_TZ=UTC", "CRON_TZ=America/New_York", "CRON_TZ=Asia/Kolkata"}).Draw(t, "tzPrefix")
	}
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
		windows = append(windows, genWindow(t, namePrefix, false))
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
		window := genWindow(t, "smoke", false)
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
		window := genWindow(t, "p1", false)
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
	t.Run("windowActiveAtIsTotal", func(t *testing.T) {
		// windowActiveAt is total
		rapid.Check(t, func(t *rapid.T) {
			window := genWindow(t, "p2", false)
			now := drawHostileOrWideInstant(t)
			_, err := windowActiveAt(window, now)
			require.NoError(t, err)
		})
	})
	t.Run("effectiveTargetAtIsTotal", func(t *testing.T) {
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
	})
}

// TestEffectiveTargetAtIsOrderAndDuplicateInvariant is property 4 (Stage 1):
// the effective target must not depend on the order windows are listed in
// (a config comes from a CRD list, whose order is not meaningful), and must
// not change if a window happens to appear twice. Both halves self-compare
// against the original config rather than using the oracle, since what's
// being checked is invariance under a transformation, not correctness
// against ground truth. The duplicate check deliberately builds its list
// from the original (unshuffled) windows, not the already-shuffled ones, so
// a failure there can only mean duplicate-handling is broken — not a
// leftover order-handling bug resurfacing under a different shuffle.
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

// TestWindowActiveAtIsTimezoneInvariantForPrefixedSpecs is property 5
// (Stage 1): windowActiveAt is deterministic (same inputs, same output every
// time), and a window whose cron carries an explicit CRON_TZ prefix must
// answer the same way regardless of what time.Location the passed-in instant
// happens to be expressed in — the same real moment, relabeled, must not
// change the answer, since the spec named its own timezone explicitly.
//
// This invariance is deliberately NOT asserted for un-prefixed specs. An
// un-prefixed cron is evaluated in whatever location the passed-in instant
// carries — i.e. it follows the caller's (in production, the executor
// process's) timezone. That is intended, documented behaviour, not a gap:
// users who want a fixed zone write CRON_TZ=. Only prefixed specs get the
// invariance check here.
func TestWindowActiveAtIsTimezoneInvariantForPrefixedSpecs(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		window := genWindow(t, "p5", true)
		instant := drawHostileOrWideInstant(t)
		location := rapid.SampledFrom([]string{"UTC", "America/New_York", "Asia/Kolkata", "Australia/Lord_Howe"}).Draw(t, "location")
		otherLoc, err := time.LoadLocation(location)
		require.NoError(t, err)
		instantOther := instant.In(otherLoc)
		w1, err := windowActiveAt(window, instant)
		require.NoError(t, err)
		w2, err := windowActiveAt(window, instantOther)
		require.NoError(t, err)
		require.Equal(t, w1, w2, "window=%+v instant=%s otherLoc=%s", window, instant, otherLoc)

		// trivial determinism
		wA, err := windowActiveAt(window, instant)
		require.NoError(t, err)
		wB, err := windowActiveAt(window, instant)
		require.NoError(t, err)

		require.Equal(t, wA, wB, "window=%+v instant=%s", window, instant)
	})
}

// TestWindowActiveAtCapsDenseCronAsActive is property 6 (Stage 1): when a
// window's cron fires far more often than its duration lasts, the walk to
// find the most recent fire hits lastSched's maxScheduleIterations cap, and
// windowActiveAt must report the window as active in that case (the give-up
// path's documented contract — a cron this dense can never fully close).
// genDenseWindow forces minute and hour to "*" and picks a duration large
// enough to guarantee the cap is hit across several second-level densities
// (*, */5, */10), so the give-up path's return value is exercised under more
// than the one hand-picked shape the existing example test covers.
func TestWindowActiveAtCapsDenseCronAsActive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		window := genDenseWindow(t, "p6")
		instant := drawUniformInstant(t)
		got, err := windowActiveAt(window, instant)
		require.NoError(t, err)
		require.True(t, got, "window=%+v instant=%s", window, instant)
	})
}

// runMalformedWindowSubtest is the shared body for property 7's three
// malformation kinds: draw a valid config, record its baseline target,
// corrupt a copy of one existing window via corrupt, insert that copy at a
// random position, and require the target is unchanged and exactly one
// error names the bad window.
func runMalformedWindowSubtest(t *testing.T, corrupt func(*fv1.ProvisionedWindow)) {
	rapid.Check(t, func(t *rapid.T) {
		windows := drawWindows(t, "p7", 4)
		baseTarget := rapid.IntRange(0, 10).Draw(t, "baseTarget")
		cfg := fv1.ProvisionedConcurrencyConfig{
			Target:  baseTarget,
			Windows: windows,
		}
		instant := drawUniformInstant(t)
		originalTarget, errs := effectiveTargetAt(&cfg, instant)
		require.Equal(t, 0, len(errs))

		if len(windows) == 0 {
			return
		}
		randIndex := rapid.IntRange(0, len(windows)-1).Draw(t, "randIndex")
		randWindow := windows[randIndex]
		corrupt(&randWindow)
		randInsert := rapid.IntRange(0, len(windows)).Draw(t, "randInsert")
		windows = slices.Insert(windows, randInsert, randWindow)
		cfgNew := fv1.ProvisionedConcurrencyConfig{
			Target:  baseTarget,
			Windows: windows,
		}
		newTarget, errs := effectiveTargetAt(&cfgNew, instant)
		require.Equal(t, 1, len(errs))
		require.Contains(t, errs[0].Error(), randWindow.Name)
		require.Equal(t, originalTarget, newTarget, "cfg=%+v newcfg=%+v instant=%s", cfg, cfgNew, instant)
	})
}

// TestEffectiveTargetAtIsolatesMalformedWindow is property 7 (Stage 1): a
// single malformed window injected anywhere into an otherwise-valid config
// must not change the effective target, and must appear as exactly one
// named error — one bad window (unparseable cron, unparseable duration, or
// non-positive duration; three separate failure paths in windowActiveAt)
// must never take down evaluation of the rest of the config.
func TestEffectiveTargetAtIsolatesMalformedWindow(t *testing.T) {
	t.Run("ZeroDuration", func(t *testing.T) {
		runMalformedWindowSubtest(t, func(w *fv1.ProvisionedWindow) {
			w.Duration = "0"
		})
	})
	t.Run("BadDuration", func(t *testing.T) {
		runMalformedWindowSubtest(t, func(w *fv1.ProvisionedWindow) {
			w.Duration = "BadDuration"
		})
	})
	t.Run("BadCron", func(t *testing.T) {
		runMalformedWindowSubtest(t, func(w *fv1.ProvisionedWindow) {
			w.Start = "BadCron"
		})
	})
}
