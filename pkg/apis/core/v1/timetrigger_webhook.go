/*
Copyright 2022.

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

package v1

import (
	"github.com/robfig/cron"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	ferror "github.com/fission/fission/pkg/error"
)

// log is for logging in this package.
var timetriggerlog = logf.Log.WithName("timetrigger-resource")

func (r *TimeTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-fission-io-v1-timetrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=timetriggers,verbs=create;update,versions=v1,name=mtimetrigger.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &TimeTrigger{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *TimeTrigger) Default() {
	timetriggerlog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-timetrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=timetriggers,verbs=create;update,versions=v1,name=vtimetrigger.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &TimeTrigger{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *TimeTrigger) ValidateCreate() error {
	timetriggerlog.Info("validate create", "name", r.Name)
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("TimeTrigger", err)
		return err
	}

	_, err = cron.Parse(r.Spec.Cron)
	if err != nil {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "TimeTrigger cron spec is not valid")
		return err
	}
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *TimeTrigger) ValidateUpdate(old runtime.Object) error {
	timetriggerlog.Info("validate update", "name", r.Name)
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("TimeTrigger", err)
		return err
	}

	_, err = cron.Parse(r.Spec.Cron)
	if err != nil {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "TimeTrigger cron spec is not valid")
		return err
	}

	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *TimeTrigger) ValidateDelete() error {
	timetriggerlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil
}
