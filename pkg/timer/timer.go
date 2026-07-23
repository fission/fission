// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"context"

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
func (timer *Timer) addUpdate(timeTrigger *fv1.TimeTrigger) {
	key := types.NamespacedName{Namespace: timeTrigger.Namespace, Name: timeTrigger.Name}
	logger := timer.logger.WithValues("trigger_name", timeTrigger.Name, "trigger_namespace", timeTrigger.Namespace)

	if item, ok := timer.triggers[key]; ok {
		if item.cron != nil {
			item.cron.Stop()
		}
		item.trigger = *timeTrigger
		item.cron = timer.newCron(*timeTrigger, timer.routerUrl)
		logger.V(1).Info("cron updated")
		return
	}
	timer.triggers[key] = &timerTriggerWithCron{
		trigger: *timeTrigger,
		cron:    timer.newCron(*timeTrigger, timer.routerUrl),
	}
	logger.V(1).Info("cron added")
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
// fires at: UrlForFunctionRef(name, namespace, suffix) + Subpath, where
// suffix is the reference's Alias if set, else its Version (mutually
// exclusive, CEL/webhook-enforced) -- extracted from newCron so it is
// unit-testable without spinning up a cron.Cron.
//
// TimeTriggerSpec embeds FunctionReference (json "functionref"), so
// Alias/Version are promoted fields, read the same way t.Spec.Name already
// is. Resolution of what the suffix routes to stays entirely router-side --
// this only builds a URL string.
func functionTargetURL(t fv1.TimeTrigger) string {
	suffix := t.Spec.Alias
	if suffix == "" {
		suffix = t.Spec.Version
	}
	return utils.UrlForFunctionRef(t.Spec.Name, t.Namespace, suffix) + t.Spec.Subpath
}

func (timer *Timer) newCron(t fv1.TimeTrigger, routerUrl string) *cron.Cron {
	target := functionTargetURL(t)

	// create one publisher per-cron timer
	timerPublisher := publisher.MakeWebhookPublisher(timer.logger, routerUrl)

	c := cron.New(
		cron.WithParser(
			cron.NewParser(
				cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)))
	c.AddFunc(t.Spec.Cron, func() { //nolint: errCheck
		headers := map[string]string{
			"X-Fission-Timer-Name": t.Name,
		}

		// with the addition of multi-tenancy, the users can create functions in any namespace. however,
		// the triggers can only be created in the same namespace as the function.
		// so essentially, function namespace = trigger namespace.
		(timerPublisher).Publish(context.Background(), "", headers, t.Spec.Method, target)
	})
	c.Start()
	timer.logger.Info("started cron for time trigger", "trigger_name", t.Name, "trigger_namespace", t.Namespace, "cron", t.Spec.Cron)
	return c
}
