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

type requestType int

const (
	SYNC requestType = iota
)

type (
	Timer struct {
		logger    logr.Logger
		triggers  map[types.UID]*timerTriggerWithCron
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
		triggers:  make(map[types.UID]*timerTriggerWithCron),
		routerUrl: routerUrl,
	}
	return timer
}

func (timer *Timer) newCron(t fv1.TimeTrigger, routerUrl string) *cron.Cron {
	target := utils.UrlForFunction(t.Spec.Name, t.Namespace) + t.Spec.Subpath

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
