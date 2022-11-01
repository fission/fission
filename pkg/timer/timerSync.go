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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func (ws *TimerSync) TimeTriggerEventHandlers(ctx context.Context) {
	for _, informer := range ws.timeTriggerInformer {
		informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {

				objTimeTrigger := obj.(*fv1.TimeTrigger)
				ws.logger.Debug("cron added", zap.String("trigger name", objTimeTrigger.ObjectMeta.Name))
				ws.timer.triggers[crd.CacheKey(&objTimeTrigger.ObjectMeta)] = &timerTriggerWithCron{
					trigger: *objTimeTrigger,
					cron:    ws.timer.newCron(*objTimeTrigger),
				}
			},
			UpdateFunc: func(oldObj interface{}, newObj interface{}) {
				oldTimeTrigger := oldObj.(*fv1.TimeTrigger)
				newTimeTrigger := newObj.(*fv1.TimeTrigger)
				if item, ok := ws.timer.triggers[crd.CacheKey(&newTimeTrigger.ObjectMeta)]; ok {
					if oldTimeTrigger.Spec.Cron != newTimeTrigger.Spec.Cron {
						if item.cron != nil {
							item.cron.Stop()
						}
						item.cron = ws.timer.newCron(*newTimeTrigger)
						ws.logger.Debug("cron updated", zap.String("trigger name", newTimeTrigger.ObjectMeta.Name))
					}
					ws.logger.Debug("old spec and new spec are not same", zap.String("trigger name", newTimeTrigger.ObjectMeta.Name))
				}
				ws.logger.Debug("time trigger not found in update", zap.String("trigger name", newTimeTrigger.ObjectMeta.Name))
			},
			DeleteFunc: func(obj interface{}) {
				objTimeTrigger := obj.(*fv1.TimeTrigger)
				if item, ok := ws.timer.triggers[crd.CacheKey(&objTimeTrigger.ObjectMeta)]; ok {
					if item.cron != nil {
						item.cron.Stop()
						ws.logger.Info("cron for time trigger stopped", zap.String("trigger", item.trigger.ObjectMeta.Name))
					}
					delete(ws.timer.triggers, crd.CacheKey(&objTimeTrigger.ObjectMeta))
					ws.logger.Debug("time trigger deleted from cache", zap.String("trigger name", objTimeTrigger.ObjectMeta.Name))
				}
				ws.logger.Debug("time trigger not found in detele", zap.String("trigger name", objTimeTrigger.ObjectMeta.Name))
			},
		})
	}
}

func (ws *TimerSync) syncSvc(ctx context.Context, namespace string) {
	for {
		triggers, err := ws.fissionClient.CoreV1().TimeTriggers(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if utils.IsNetworkError(err) {
				ws.logger.Info("encountered a network error - will retry", zap.Error(err))
				time.Sleep(5 * time.Second)
				continue
			}
			ws.logger.Fatal("failed to get time trigger list", zap.Error(err), zap.String("namespace", namespace))
		}
		ws.timer.Sync(triggers.Items) //nolint: errCheck

		// TODO switch to watches
		time.Sleep(3 * time.Second)
	}
}
