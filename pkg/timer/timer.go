/*
Copyright 2017 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package timer

import (
	"github.com/robfig/cron"
	"go.uber.org/zap"

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
		logger    *zap.Logger
		triggers  map[string]*timerTriggerWithCron
		publisher *publisher.Publisher
	}

	timerTriggerWithCron struct {
		trigger fv1.TimeTrigger
		cron    *cron.Cron
	}
)

func MakeTimer(logger *zap.Logger, publisher publisher.Publisher) *Timer {
	timer := &Timer{
		logger:    logger.Named("timer"),
		triggers:  make(map[string]*timerTriggerWithCron),
		publisher: &publisher,
	}
	return timer
}

func (timer *Timer) newCron(t fv1.TimeTrigger) *cron.Cron {
	c := cron.New()
	c.AddFunc(t.Spec.Cron, func() { //nolint: errCheck
		headers := map[string]string{
			"X-Fission-Timer-Name": t.ObjectMeta.Name,
		}

		// with the addition of multi-tenancy, the users can create functions in any namespace. however,
		// the triggers can only be created in the same namespace as the function.
		// so essentially, function namespace = trigger namespace.
		(*timer.publisher).Publish("", headers, utils.UrlForFunction(t.Spec.FunctionReference.Name, t.ObjectMeta.Namespace))
	})
	c.Start()
	timer.logger.Info("added new cron for time trigger", zap.String("trigger", t.ObjectMeta.Name))
	return c
}
