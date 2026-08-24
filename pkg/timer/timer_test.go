// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// trigger builds a TimeTrigger with a cron spec and a creation time.
func trigger(spec string, created time.Time) fv1.TimeTrigger {
	tt := fv1.TimeTrigger{Spec: fv1.TimeTriggerSpec{Cron: spec}}
	tt.Name, tt.Namespace = "tt", "default"
	if !created.IsZero() {
		tt.CreationTimestamp = metav1.NewTime(created)
	}
	return tt
}

// TestAnchoredScheduleNext pins the phase arithmetic: firings are always
// anchor + k*interval, and always strictly after the time asked about.
func TestAnchoredScheduleNext(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := anchoredSchedule{interval: 6 * time.Hour, anchor: anchor}

	for _, tc := range []struct {
		name string
		from time.Time
		want time.Time
	}{
		// Clock skew: the API server sets CreationTimestamp, so a timer pod whose
		// clock trails it sees a trigger created in its own future. It must still
		// wait a full interval, like every subsequent firing.
		{"before the anchor still waits a full interval", anchor.Add(-time.Hour), anchor.Add(6 * time.Hour)},
		{"at the anchor moves a full interval", anchor, anchor.Add(6 * time.Hour)},
		{"mid-interval snaps to the boundary", anchor.Add(7 * time.Hour), anchor.Add(12 * time.Hour)},
		{"exactly on a boundary moves to the next", anchor.Add(12 * time.Hour), anchor.Add(18 * time.Hour)},
		{"far in the future stays in phase", anchor.Add(100*time.Hour + 30*time.Minute), anchor.Add(102 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := s.Next(tc.from)
			assert.Equal(t, tc.want, got)
			assert.True(t, got.After(tc.from), "Next must be strictly after the time asked about")
		})
	}
}

// TestAnchoredScheduleNextIsAlwaysFuture guards the one way this schedule could
// wedge the scheduler: cron fires whatever Next returns and then asks again, so
// a Next in the past becomes an unbounded fire loop hammering the function.
// time.Sub saturates rather than wrapping, so a far-past anchor would otherwise
// overflow the multiply into a negative offset.
func TestAnchoredScheduleNextIsAlwaysFuture(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, anchor := range []time.Time{
		now.Add(-time.Second),
		now.Add(-100 * 365 * 24 * time.Hour),
		time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC),
		// the zero time, ~year 1
		{},
	} {
		s := anchoredSchedule{interval: 24 * time.Hour, anchor: anchor}
		assert.Truef(t, s.Next(now).After(now), "anchor %v produced a non-future firing %v", anchor, s.Next(now))
	}
}

// TestAnchoredScheduleSurvivesRestart is the #2660 fix stated as a property:
// the firing times do not depend on when the process asked.
//
// This is the half the issue does not mention and that matters more. With the
// stock ConstantDelaySchedule the phase resets on every restart, so a timer pod
// restarting more often than the interval starves the trigger completely — an
// "@every 24h" trigger under a daily rollout never fires at all.
func TestAnchoredScheduleSurvivesRestart(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := anchoredSchedule{interval: 24 * time.Hour, anchor: anchor}
	stock := cron.ConstantDelaySchedule{Delay: 24 * time.Hour}

	// A pod that restarts every 20 hours, five times over.
	var anchoredFires, stockFires int
	for i := 1; i <= 5; i++ {
		restart := anchor.Add(time.Duration(i) * 20 * time.Hour)
		nextRestart := restart.Add(20 * time.Hour)
		if s.Next(restart).Before(nextRestart) {
			anchoredFires++
		}
		if stock.Next(restart).Before(nextRestart) {
			stockFires++
		}
	}
	assert.Positive(t, anchoredFires, "an anchored schedule keeps firing across restarts")
	assert.Zero(t, stockFires, "the stock schedule never fires when restarts outpace the interval — the bug being fixed")
}

// TestAnchoredScheduleDesynchronises covers the reported symptom: triggers
// created at different times must not collapse onto one firing instant just
// because they were registered together at process start.
func TestAnchoredScheduleDesynchronises(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	start := base.Add(50 * time.Hour) // a much later process start
	fires := map[time.Time]bool{}
	for _, offset := range []time.Duration{0, 37 * time.Minute, 2 * time.Hour, 5*time.Hour + 13*time.Minute} {
		s := anchoredSchedule{interval: 6 * time.Hour, anchor: base.Add(offset)}
		fires[s.Next(start)] = true
	}
	assert.Len(t, fires, 4, "triggers created at different times must fire at different times")
}

// TestScheduleFor covers which specs get anchored. Standard cron expressions are
// already wall-clock anchored, so wrapping them would change their meaning.
func TestScheduleFor(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("@every is anchored to the creation time", func(t *testing.T) {
		t.Parallel()
		s, err := scheduleFor(trigger("@every 6h", created))
		require.NoError(t, err)
		require.IsType(t, anchoredSchedule{}, s)
		assert.Equal(t, created.Add(6*time.Hour), s.Next(created))
	})

	t.Run("standard cron is left alone", func(t *testing.T) {
		t.Parallel()
		s, err := scheduleFor(trigger("0 */6 * * *", created))
		require.NoError(t, err)
		_, anchored := s.(anchoredSchedule)
		assert.False(t, anchored, "a wall-clock cron must keep its own phase")
	})

	t.Run("a descriptor is left alone", func(t *testing.T) {
		t.Parallel()
		s, err := scheduleFor(trigger("@daily", created))
		require.NoError(t, err)
		_, anchored := s.(anchoredSchedule)
		assert.False(t, anchored, "a wall-clock descriptor must keep its own phase")
	})

	t.Run("no creation timestamp falls back to the stock schedule", func(t *testing.T) {
		t.Parallel()
		s, err := scheduleFor(trigger("@every 6h", time.Time{}))
		require.NoError(t, err)
		assert.IsType(t, cron.ConstantDelaySchedule{}, s)
	})

	t.Run("an unparseable spec is an error, not a silent no-op", func(t *testing.T) {
		t.Parallel()
		_, err := scheduleFor(trigger("not a cron spec", created))
		assert.Error(t, err)
	})
}

// TestFunctionTargetURL is the timer publisher's wiring test for RFC-0025:
// a TimeTrigger's embedded FunctionReference.Alias/Version threads through to
// the URL the cron actually fires at, via the shared
// utils.UrlForFunctionRef helper (table-tested on its own in
// pkg/utils/utils_test.go).
func TestFunctionTargetURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ref  fv1.FunctionReference
		ns   string
		sub  string
		want string
	}{
		{
			name: "bare reference: no suffix",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
			ns:   "default",
			sub:  "/",
			want: "/fission-function/fn/",
		},
		{
			name: "alias reference: :<alias> suffix",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn", Alias: "blue"},
			ns:   "ns1",
			sub:  "/",
			want: "/fission-function/ns1/fn:blue/",
		},
		{
			name: "version reference: :<version> suffix",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn", Version: "fn-v3"},
			ns:   "ns1",
			sub:  "/sub",
			want: "/fission-function/ns1/fn:fn-v3/sub",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tt := fv1.TimeTrigger{
				Name: "tt", Namespace: tc.ns,
				Spec: fv1.TimeTriggerSpec{
					FunctionReference: tc.ref,
					Subpath:           tc.sub,
				},
			}
			assert.Equal(t, tc.want, functionTargetURL(tt))
		})
	}
}
