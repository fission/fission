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

package webhook

import (
	"context"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type TimeTrigger struct{}

// log is for logging in this package.
var timetriggerlog = loggerfactory.GetLogger().Named("timetrigger-resource")

func (r *TimeTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.TimeTrigger{}).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-timetrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=timetriggers,verbs=create;update,versions=v1,name=mtimetrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &TimeTrigger{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *TimeTrigger) Default(_ context.Context, obj runtime.Object) error {
	timetriggerlog.Debug("default", zap.String("name", r.Name))
}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-timetrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=timetriggers,verbs=create;update,versions=v1,name=vtimetrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &TimeTrigger{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *TimeTrigger) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	timetriggerlog.Debug("validate create", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("TimeTrigger", err)
		return nil, err
	}

	err = IsValidCronSpec(r.Spec.Cron)
	if err != nil {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "TimeTrigger cron spec is not valid")
		return nil, err
	}
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *TimeTrigger) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	timetriggerlog.Debug("validate update", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("TimeTrigger", err)
		return nil, err
	}

	err = IsValidCronSpec(r.Spec.Cron)
	if err != nil {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "TimeTrigger cron spec is not valid")
		return nil, err
	}

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *TimeTrigger) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	timetriggerlog.Debug("validate delete", zap.String("name", r.Name))
	return nil, nil
}
