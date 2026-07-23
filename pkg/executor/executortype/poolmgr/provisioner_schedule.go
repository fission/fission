// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

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
		iterations := 0
		for {
			if iterations >= 100_000 {
				return true, nil
			}
			t2 := sched.Next(t)
			if t2.After(now) {
				break
			}
			t = t2
			iterations++
		}
		return now.Sub(t) < dur, nil
	}

}
