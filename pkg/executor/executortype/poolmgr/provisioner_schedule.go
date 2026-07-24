// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/robfig/cron/v3"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

type perWindow struct {
	name        string
	schedule    cron.Schedule
	activeUntil time.Time
	nextOpen    time.Time
	capped      bool
}

type fnSchedule struct {
	name       string
	namespace  string
	generation int64
	windows    map[string]*perWindow
	timer      *time.Timer
	mu         sync.Mutex
}

func (f *fnSchedule) reBuildWindows(fn *fv1.Function, logger logr.Logger) {
	f.windows = make(map[string]*perWindow)
	cronSpecParser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	for _, window := range fn.Spec.ProvisionedConcurrency.Windows {
		sched, err := cronSpecParser.Parse(window.Start)
		if err != nil {
			logger.Error(err, "cron parser failed to parse", "function", fn.Name, "namespace", fn.Namespace, "window", window.Name)
			f.windows[window.Name] = nil
			continue
		}
		dur, err := time.ParseDuration(window.Duration)
		if err != nil {
			logger.Error(err, "time duration parse failed to parse", "function", fn.Name, "namespace", fn.Namespace, "window", window.Name)
			f.windows[window.Name] = nil
			continue
		} else if dur <= 0 {
			logger.Error(fmt.Errorf("window duration must be positive"), "time duration must >0", "function", fn.Name, "namespace", fn.Namespace, "window", window.Name)
			f.windows[window.Name] = nil
			continue
		}
		now := time.Now()
		pw := &perWindow{name: window.Name, schedule: sched}
		t := sched.Next(now.Add(-dur))
		if t.IsZero() || t.After(now) {
			// window not active
			pw.activeUntil = time.Time{}
			pw.nextOpen = sched.Next(now)
		} else {
			// window active
			t, capped := lastSched(sched, t, now)
			pw.capped = capped
			if capped {
				pw.activeUntil = time.Time{}
			} else {
				pw.activeUntil = t.Add(dur)
			}
			pw.nextOpen = time.Time{}
		}
		f.windows[window.Name] = pw
	}
}

// effectiveTargetAt returns the effective provisioned target for cfg at
// instant now: cfg.Target if no window is currently active, or the max
// Target across all currently-active windows. If activeWindow.Target <= base.Target, activeWindow wins over base.
// Pure: no I/O,
// no wall-clock reads, so property/DST tests can hammer it directly.
// Windows that fail to evaluate (malformed cron/duration, e.g. from a raw
// kubectl write bypassing admission) are skipped rather than aborting the
// whole evaluation, and returned in badWindows for the caller to log.
func effectiveTargetAt(cfg *fv1.ProvisionedConcurrencyConfig, now time.Time) (int, []error) {
	target := cfg.Target
	badWindows := []error{}
	maxActiveTarget := 0
	active := false
	for _, window := range cfg.Windows {
		if windowActive, err := windowActiveAt(window, now); err != nil {
			e := fmt.Errorf("window %s failed: %w", window.Name, err)
			badWindows = append(badWindows, e)
		} else if windowActive {
			maxActiveTarget = max(maxActiveTarget, window.Target)
			active = true
		}
	}
	if active {
		target = maxActiveTarget
	}
	return target, badWindows
}

// windowActiveAt reports whether window is open at instant now. robfig/cron's
// Schedule only exposes Next(t) — "next fire strictly after t" — so the most
// recent fire <= now is found by walking forward from a safe lower bound
// (now-duration) until the next candidate would overshoot now. A cron denser
// than duration is capped at maxScheduleIterations walks and treated as
// active, since that density alone implies the window can never fully close.
func windowActiveAt(window fv1.ProvisionedWindow, now time.Time) (bool, error) {
	cronSpecParser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := cronSpecParser.Parse(window.Start)
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
	t := sched.Next(now.Add(-dur))
	if t.IsZero() || t.After(now) {
		return false, nil
	} else {
		var capped bool
		t, capped = lastSched(sched, t, now)
		if capped {
			return true, nil
		}
		return now.Sub(t) < dur, nil
	}
}

func lastSched(sched cron.Schedule, t time.Time, now time.Time) (time.Time, bool) {
	iterations := 0
	for {
		if iterations >= 100_000 {
			return time.Time{}, true
		}
		t2 := sched.Next(t)
		if t2.After(now) {
			break
		}
		t = t2
		iterations++
	}
	return t, false
}

func nextTransitionAt(cfg *fv1.ProvisionedConcurrencyConfig, now time.Time) (time.Time, []error) {
	badWindows := []error{}
	cronSpecParser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	var nextTransition time.Time
	for _, window := range cfg.Windows {
		var nextTime time.Time
		sched, err := cronSpecParser.Parse(window.Start)
		if err != nil {
			badWindows = append(badWindows, fmt.Errorf("sched failed: window %s failed: %w", window.Name, err))
			continue
		}
		if windowActive, err := windowActiveAt(window, now); err != nil {
			badWindows = append(badWindows, err)
			continue
		} else if !windowActive {
			nextTime = sched.Next(now)
		} else if windowActive {
			// ignoreing error since it's already been checked in windowActiveAt
			dur, _ := time.ParseDuration(window.Duration)
			t := sched.Next(now.Add(-dur)) // arbitrary long duration to find last start
			if t.IsZero() || t.After(now) {
				nextTime = sched.Next(now)
			} else {
				t, capped := lastSched(sched, t, now)
				if capped {
					nextTime = now
				} else {
					nextTime = t.Add(dur)
				}
			}
		}
		if nextTransition.IsZero() || nextTime.Before(nextTransition) {
			nextTransition = nextTime
		}
	}

	return nextTransition, badWindows
}
