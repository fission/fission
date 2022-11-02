/*
Copyright 2016 The Fission Authors.

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
	"context"
	"time"

	"go.uber.org/zap"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

type (
	TimerSync struct {
		logger              *zap.Logger
		fissionClient       versioned.Interface
		timer               *Timer
		timeTriggerInformer map[string]k8sCache.SharedIndexInformer
	}
)

func MakeTimerSync(ctx context.Context, logger *zap.Logger, fissionClient versioned.Interface, timer *Timer) *TimerSync {
	ws := &TimerSync{
		logger:        logger.Named("timer_sync"),
		fissionClient: fissionClient,
		timer:         timer,
	}
	ws.timeTriggerInformer = utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.TimeTriggerResource)
	ws.TimeTriggerEventHandlers(ctx)
	return ws
}

func (ws *TimerSync) Run(ctx context.Context) {
	for _, informer := range ws.timeTriggerInformer {
		go informer.Run(ctx.Done())
	}
}

func (ws *TimerSync) AddUpdateTimeTrigger(timeTrigger *fv1.TimeTrigger) {
	logger := ws.logger.With(zap.String("trigger_name", timeTrigger.Name), zap.String("trigger_namespace", timeTrigger.Namespace))

	ws.logger.Debug("cron event")
	ws.timer.triggers[crd.CacheKey(&timeTrigger.ObjectMeta)] = &timerTriggerWithCron{
		trigger: *timeTrigger,
		cron:    ws.timer.newCron(*timeTrigger),
	}

	if item, ok := ws.timer.triggers[crd.CacheKey(&timeTrigger.ObjectMeta)]; ok {
		if item.trigger.Spec.Cron != timeTrigger.Spec.Cron {
			if item.cron != nil {
				item.cron.Stop()
			}
			item.cron = ws.timer.newCron(*timeTrigger)
			logger.Debug("cron updated")
		}
	} else {
		ws.timer.triggers[crd.CacheKey(&timeTrigger.ObjectMeta)] = &timerTriggerWithCron{
			trigger: *timeTrigger,
			cron:    ws.timer.newCron(*timeTrigger),
		}
		logger.Debug("cron added")
	}
}

func (ws *TimerSync) DeleteTimeTrigger(timeTrigger *fv1.TimeTrigger) {
	logger := ws.logger.With(zap.String("trigger_name", timeTrigger.Name), zap.String("trigger_namespace", timeTrigger.Namespace))

	if item, ok := ws.timer.triggers[crd.CacheKey(&timeTrigger.ObjectMeta)]; ok {
		if item.cron != nil {
			item.cron.Stop()
			logger.Info("cron for time trigger stopped")
		}
		delete(ws.timer.triggers, crd.CacheKey(&timeTrigger.ObjectMeta))
		logger.Debug("cron deleted")
	}
}

func (ws *TimerSync) TimeTriggerEventHandlers(ctx context.Context) {
	for _, informer := range ws.timeTriggerInformer {
		informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				timeTrigger := obj.(*fv1.TimeTrigger)
				ws.AddUpdateTimeTrigger(timeTrigger)
			},
			UpdateFunc: func(_ interface{}, obj interface{}) {
				timeTrigger := obj.(*fv1.TimeTrigger)
				ws.AddUpdateTimeTrigger(timeTrigger)
			},
			DeleteFunc: func(obj interface{}) {
				timeTrigger := obj.(*fv1.TimeTrigger)
				ws.DeleteTimeTrigger(timeTrigger)
			},
		})
	}
}
