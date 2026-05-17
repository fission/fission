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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

type (
	TimerSync struct {
		logger              logr.Logger
		fissionClient       versioned.Interface
		timer               *Timer
		timeTriggerInformer map[string]k8sCache.SharedIndexInformer
		// rootCtx is the long-running TimerSync context, captured at
		// MakeTimerSync. The cache event-handler callbacks
		// (AddUpdate/DeleteTimeTrigger) have no ctx parameter, so
		// status writes from those callbacks scope their requests to
		// this ctx — when the controller shuts down, dangling status
		// writes are cancelled rather than left to fail on their own.
		rootCtx context.Context
	}
)

func MakeTimerSync(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, timer *Timer) (*TimerSync, error) {
	ws := &TimerSync{
		logger:        logger.WithName("timer_sync"),
		fissionClient: fissionClient,
		timer:         timer,
		rootCtx:       ctx,
	}
	ws.timeTriggerInformer = utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.TimeTriggerResource)
	err := ws.TimeTriggerEventHandlers(ctx)
	if err != nil {
		return nil, err
	}
	return ws, nil
}

func (ws *TimerSync) Run(ctx context.Context, mgr manager.Interface) {
	mgr.AddInformers(ctx, ws.timeTriggerInformer)
}

func (ws *TimerSync) AddUpdateTimeTrigger(timeTrigger *fv1.TimeTrigger) {
	logger := ws.logger.WithValues("trigger_name", timeTrigger.Name, "trigger_namespace", timeTrigger.Namespace)

	ws.logger.V(1).Info("cron event")

	if item, ok := ws.timer.triggers[crd.CacheKeyUIDFromMeta(&timeTrigger.ObjectMeta)]; ok {
		if item.cron != nil {
			item.cron.Stop()
		}
		item.trigger = *timeTrigger
		item.cron = ws.timer.newCron(*timeTrigger, ws.timer.routerUrl)
		logger.V(1).Info("cron updated")
	} else {
		ws.timer.triggers[crd.CacheKeyUIDFromMeta(&timeTrigger.ObjectMeta)] = &timerTriggerWithCron{
			trigger: *timeTrigger,
			cron:    ws.timer.newCron(*timeTrigger, ws.timer.routerUrl),
		}
		logger.V(1).Info("cron added")
	}
	ws.markTimeTriggerScheduled(ws.rootCtx, timeTrigger)
}

// markTimeTriggerScheduled writes Scheduled + Ready conditions on a TimeTrigger
// after its cron entry is registered. Best-effort: timer scheduling is not
// gated on status writes. Fast-path skips Get + UpdateStatus when this
// trigger's in-memory conditions already match the desired state.
func (ws *TimerSync) markTimeTriggerScheduled(ctx context.Context, trigger *fv1.TimeTrigger) {
	if ws.fissionClient == nil {
		return
	}
	wantSched := metav1.Condition{
		Type: fv1.TimeTriggerConditionScheduled, Status: metav1.ConditionTrue,
		ObservedGeneration: trigger.Generation,
		Reason:             fv1.TimeTriggerReasonCronRegistered,
		Message:            "timer registered cron schedule " + trigger.Spec.Cron,
	}
	wantReady := metav1.Condition{
		Type: fv1.TimeTriggerConditionReady, Status: metav1.ConditionTrue,
		ObservedGeneration: trigger.Generation,
		Reason:             fv1.TimeTriggerReasonCronRegistered,
		Message:            "trigger is firing on schedule",
	}
	if conditions.IsAt(trigger.Status.Conditions, wantSched) && conditions.IsAt(trigger.Status.Conditions, wantReady) {
		return
	}
	cur, err := ws.fissionClient.CoreV1().TimeTriggers(trigger.Namespace).Get(ctx, trigger.Name, metav1.GetOptions{})
	if err != nil {
		ws.logger.V(1).Info("timetrigger status: get failed", "name", trigger.Name, "namespace", trigger.Namespace, "error", err)
		return
	}
	wantSched.ObservedGeneration = cur.Generation
	wantReady.ObservedGeneration = cur.Generation
	if conditions.IsAt(cur.Status.Conditions, wantSched) && conditions.IsAt(cur.Status.Conditions, wantReady) {
		return
	}
	conditions.Set(&cur.Status.Conditions, wantSched)
	conditions.Set(&cur.Status.Conditions, wantReady)
	if _, err := ws.fissionClient.CoreV1().TimeTriggers(trigger.Namespace).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		ws.logger.V(1).Info("timetrigger status: update failed", "name", trigger.Name, "namespace", trigger.Namespace, "error", err)
	}
}

func (ws *TimerSync) DeleteTimeTrigger(timeTrigger *fv1.TimeTrigger) {
	logger := ws.logger.WithValues("trigger_name", timeTrigger.Name, "trigger_namespace", timeTrigger.Namespace)

	if item, ok := ws.timer.triggers[crd.CacheKeyUIDFromMeta(&timeTrigger.ObjectMeta)]; ok {
		if item.cron != nil {
			item.cron.Stop()
			logger.Info("cron for time trigger stopped")
		}
		delete(ws.timer.triggers, crd.CacheKeyUIDFromMeta(&timeTrigger.ObjectMeta))
		logger.V(1).Info("cron deleted")
	}
}

func (ws *TimerSync) TimeTriggerEventHandlers(ctx context.Context) error {
	for _, informer := range ws.timeTriggerInformer {
		_, err := informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				timeTrigger := obj.(*fv1.TimeTrigger)
				ws.AddUpdateTimeTrigger(timeTrigger)
			},
			UpdateFunc: func(oldObj any, newObj any) {
				oldTimeTrigger := oldObj.(*fv1.TimeTrigger)
				newTimeTrigger := newObj.(*fv1.TimeTrigger)
				// Compare Generation, not ResourceVersion: our condition
				// write at the end of AddUpdateTimeTrigger goes through
				// the status subresource and bumps RV only. Re-running
				// AddUpdateTimeTrigger on a status-only update would
				// cycle the cron entry needlessly.
				if oldTimeTrigger.Generation != newTimeTrigger.Generation {
					ws.AddUpdateTimeTrigger(newTimeTrigger)
				}
			},
			DeleteFunc: func(obj any) {
				timeTrigger := obj.(*fv1.TimeTrigger)
				ws.DeleteTimeTrigger(timeTrigger)
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}
