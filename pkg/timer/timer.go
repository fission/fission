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
	"context"

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/publisher"
	"github.com/fission/fission/pkg/utils"
)

var fissionTimerFinalizer = "fission.io/fission-timer"

type (
	timerTriggerWithCron struct {
		trigger fv1.TimeTrigger
		cron    *cron.Cron
	}

	reconcileTimer struct {
		client    client.Client
		scheme    *runtime.Scheme
		triggers  map[types.UID]*timerTriggerWithCron
		publisher *publisher.WebhookPublisher
	}
)

func (r *reconcileTimer) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := log.FromContext(ctx)

	timer := &fv1.TimeTrigger{}
	err := r.client.Get(ctx, req.NamespacedName, timer)
	if errors.IsNotFound(err) {
		log.Info("TimeTrigger resource not found. Ignoring since object must be deleted")
		return reconcile.Result{}, nil
	}
	if err != nil {
		log.Error(err, "Failed to get TimeTrigger resource")
		return reconcile.Result{}, err
	}

	if timer.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object. This is equivalent
		// registering our finalizer.
		if !controllerutil.ContainsFinalizer(timer, fissionTimerFinalizer) {
			controllerutil.AddFinalizer(timer, fissionTimerFinalizer)
			if err := r.client.Update(ctx, timer); err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
	} else {
		// The object is being deleted.
		if controllerutil.ContainsFinalizer(timer, fissionTimerFinalizer) {
			log.Info("Deleting TimeTrigger", "trigger_name", timer.Name, "trigger_namespace", timer.Namespace)

			// Cleanup timer object related resources
			r.reconcileDeleteTimeTrigger(ctx, timer)

			// Remove the finalizer
			controllerutil.RemoveFinalizer(timer, fissionTimerFinalizer)
			if err := r.client.Update(ctx, timer); err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
	}

	// Handle add and update event
	r.reconcileAddUpdateTimeTrigger(ctx, timer)

	return ctrl.Result{}, nil
}

func (r *reconcileTimer) reconcileAddUpdateTimeTrigger(ctx context.Context, tt *fv1.TimeTrigger) {
	log := log.FromContext(ctx)

	if item, ok := r.triggers[crd.CacheKeyUIDFromMeta(&tt.ObjectMeta)]; ok {
		if item.cron != nil {
			item.cron.Stop()
		}
		item.trigger = *tt
		item.cron = r.newCron(ctx, *tt)
		log.Info("cron updated")
	} else {
		r.triggers[crd.CacheKeyUIDFromMeta(&tt.ObjectMeta)] = &timerTriggerWithCron{
			trigger: *tt,
			cron:    r.newCron(ctx, *tt),
		}
		log.Info("cron added")
	}
}

func (r *reconcileTimer) reconcileDeleteTimeTrigger(ctx context.Context, tt *fv1.TimeTrigger) {
	log := log.FromContext(ctx)

	if item, ok := r.triggers[crd.CacheKeyUIDFromMeta(&tt.ObjectMeta)]; ok {
		if item.cron != nil {
			item.cron.Stop()
			log.Info("cron for time trigger stopped")
		}
		delete(r.triggers, crd.CacheKeyUIDFromMeta(&tt.ObjectMeta))
		log.Info("cron deleted")
	}
}

func (r *reconcileTimer) newCron(ctx context.Context, t fv1.TimeTrigger) *cron.Cron {
	log := log.FromContext(ctx)

	target := utils.UrlForFunction(t.Spec.FunctionReference.Name, t.Namespace) + t.Spec.Subpath
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
		(*r.publisher).Publish(context.Background(), "", headers, t.Spec.Method, target)
	})
	c.Start()
	log.Info("started cron for time trigger", "trigger_name", t.Name, "trigger_namespace", t.Namespace, "cron", t.Spec.Cron)
	return c
}

func (r *reconcileTimer) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fv1.TimeTrigger{}).
		Complete(r)
}
