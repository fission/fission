// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"context"
	"math"
	"time"

	"github.com/go-logr/logr"
	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/publisher"
	"github.com/fission/fission/pkg/utils"
)

type (
	Timer struct {
		logger    logr.Logger
		triggers  map[types.NamespacedName]*timerTriggerWithCron
		routerUrl string
	}

	timerTriggerWithCron struct {
		trigger fv1.TimeTrigger
		cron    *cron.Cron
	}
)

func MakeTimer(logger logr.Logger, routerUrl string) *Timer {
	timer := &Timer{
		logger:    logger.WithName("timer"),
		triggers:  make(map[types.NamespacedName]*timerTriggerWithCron),
		routerUrl: routerUrl,
	}
	return timer
}

// addUpdate (re)registers the cron entry for a time trigger. An existing entry
// for the same trigger is stopped and replaced so a changed schedule takes
// effect. Keyed by namespaced name so the reconciler can tear it down on a
// delete (when only the name is known).
//
// Returns the schedule error rather than swallowing it: the reconciler stamps
// Scheduled/Ready=True on whatever addUpdate leaves behind, so an error absorbed
// here would surface as a trigger reported healthy that never fires.
func (timer *Timer) addUpdate(timeTrigger *fv1.TimeTrigger) error {
	key := types.NamespacedName{Namespace: timeTrigger.Namespace, Name: timeTrigger.Name}
	logger := timer.logger.WithValues("trigger_name", timeTrigger.Name, "trigger_namespace", timeTrigger.Namespace)

	c, err := timer.newCron(*timeTrigger, timer.routerUrl)
	if err != nil {
		// Drop any prior schedule: leaving the old cron running would fire the
		// trigger on a schedule the spec no longer asks for.
		timer.remove(key)
		return err
	}

	if item, ok := timer.triggers[key]; ok {
		if item.cron != nil {
			item.cron.Stop()
		}
		item.trigger = *timeTrigger
		item.cron = c
		logger.V(1).Info("cron updated")
		return nil
	}
	timer.triggers[key] = &timerTriggerWithCron{trigger: *timeTrigger, cron: c}
	logger.V(1).Info("cron added")
	return nil
}

// remove stops and drops the cron entry for a deleted time trigger. No-op if
// the trigger was never registered (e.g. a delete observed before any add).
func (timer *Timer) remove(key types.NamespacedName) {
	item, ok := timer.triggers[key]
	if !ok {
		return
	}
	if item.cron != nil {
		item.cron.Stop()
	}
	delete(timer.triggers, key)
	timer.logger.WithValues("trigger_name", key.Name, "trigger_namespace", key.Namespace).V(1).Info("cron deleted")
}

// functionTargetURL builds the internal-listener URL a TimeTrigger's cron
// fires at: UrlForFunctionReference(ref, namespace) + Subpath, where the
// alias-else-version suffix selection is UrlForFunctionReference's job --
// extracted from newCron so it is unit-testable without spinning up a
// cron.Cron.
//
// TimeTriggerSpec embeds FunctionReference (json "functionref"), so
// t.Spec.FunctionReference is that embedded value, read the same way
// t.Spec.Name already is. Resolution of what the suffix routes to stays
// entirely router-side -- this only builds a URL string.
func functionTargetURL(t fv1.TimeTrigger) string {
	return utils.UrlForFunctionReference(t.Spec.FunctionReference, t.Namespace) + t.Spec.Subpath
}

// cronParser is the spec grammar Fission accepts: standard five-field cron with
// an optional leading seconds field, plus the @descriptors (@hourly, @daily,
// @every <duration>).
var cronParser = cron.NewParser(
	cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// anchoredSchedule phase-locks a fixed-interval schedule to a fixed instant, so
// the firing times are a property of the TRIGGER rather than of whenever the
// timer process last started.
//
// robfig/cron's ConstantDelaySchedule — what "@every 6h" parses to — computes
// Next(t) as t + delay, so the phase is set by whenever the process last
// started. That both synchronises every @every trigger on restart and, when a
// pod restarts more often than the interval, starves the trigger entirely.
//
// Standard cron expressions are already wall-clock anchored and are left alone.
type anchoredSchedule struct {
	interval time.Duration
	anchor   time.Time
}

// Next returns the first firing strictly after t that is in phase with the
// anchor: the smallest anchor+k*interval greater than t. cron.Schedule requires
// strictly-after; returning t itself would spin the scheduler.
func (s anchoredSchedule) Next(t time.Time) time.Time {
	if s.interval <= 0 {
		return time.Time{} // a zero Next tells cron to never run this entry
	}
	// k is at least 1 even when t precedes the anchor. The API server sets
	// CreationTimestamp, so a timer pod whose clock trails it briefly sees a
	// trigger created in its own future; firing at the anchor itself would fire
	// the trigger seconds after creation instead of one interval later, unlike
	// every subsequent firing.
	k := int64(1)
	if !t.Before(s.anchor) {
		elapsed := t.Sub(s.anchor)
		// Guard the multiply below: time.Sub saturates at ~292 years rather than
		// wrapping, so a far-past anchor would overflow int64 and yield a time in
		// the PAST, which cron fires immediately and then recomputes forever.
		// Unreachable while CreationTimestamp is server-set, cheap to exclude.
		if int64(elapsed) > math.MaxInt64-int64(s.interval) {
			return t.Add(s.interval)
		}
		k = int64(elapsed/s.interval) + 1
	}
	return s.anchor.Add(time.Duration(k) * s.interval)
}

// scheduleFor parses a trigger's spec and, for fixed-interval schedules only,
// phase-anchors it to the trigger's creation time.
//
// A trigger with no creation timestamp (never round-tripped through the API
// server — hand-built objects, unit tests) keeps the unanchored schedule, since
// there is nothing stable to anchor to.
func scheduleFor(t fv1.TimeTrigger) (cron.Schedule, error) {
	schedule, err := cronParser.Parse(t.Spec.Cron)
	if err != nil {
		return nil, err
	}
	delay, isInterval := schedule.(cron.ConstantDelaySchedule)
	if !isInterval || t.CreationTimestamp.IsZero() {
		return schedule, nil
	}
	return anchoredSchedule{interval: delay.Delay, anchor: t.CreationTimestamp.Time}, nil
}

func (timer *Timer) newCron(t fv1.TimeTrigger, routerUrl string) (*cron.Cron, error) {
	target := functionTargetURL(t)

	// create one publisher per-cron timer
	timerPublisher := publisher.MakeWebhookPublisher(timer.logger, routerUrl)

	// No WithParser: nothing here goes through AddFunc/AddJob, which are the only
	// entry points that consult the cron's parser. scheduleFor parses instead, and
	// the schedule is installed directly with c.Schedule.
	c := cron.New()

	schedule, err := scheduleFor(t)
	if err != nil {
		// There is no TimeTrigger admission webhook — the reconciler is the gate,
		// rejecting an unparseable spec with Scheduled=False/InvalidCron before it
		// ever calls addUpdate (pkg/timer/reconciler.go). So this is belt and
		// braces against that parser and this one drifting apart. Returning the
		// error rather than discarding it, as AddFunc's was, is what keeps the
		// reconciler from stamping Ready=True on a trigger with no entries.
		return nil, err
	}

	c.Schedule(schedule, cron.FuncJob(func() {
		headers := map[string]string{
			"X-Fission-Timer-Name": t.Name,
		}

		// with the addition of multi-tenancy, the users can create functions in any namespace. however,
		// the triggers can only be created in the same namespace as the function.
		// so essentially, function namespace = trigger namespace.
		(timerPublisher).Publish(context.Background(), "", headers, t.Spec.Method, target)
	}))
	c.Start()
	timer.logger.Info("started cron for time trigger",
		"trigger_name", t.Name, "trigger_namespace", t.Namespace, "cron", t.Spec.Cron,
		"next_fire", schedule.Next(time.Now()))
	return c, nil
}
