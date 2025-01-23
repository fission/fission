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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type MessageQueueTrigger struct{}

// log is for logging in this package.
var messagequeuetriggerlog = loggerfactory.GetLogger().Named("messagequeuetrigger-resource")

func (r *MessageQueueTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.MessageQueueTrigger{}).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-messagequeuetrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=mmessagequeuetrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &MessageQueueTrigger{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *MessageQueueTrigger) Default(_ context.Context, obj runtime.Object) error {
	// messagequeuetriggerlog.Debug("default", zap.String("name", r.Name))
}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-messagequeuetrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=vmessagequeuetrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &MessageQueueTrigger{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	// messagequeuetriggerlog.Debug("validate create", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("MessageQueueTrigger", err)
		return nil, err
	}
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	// messagequeuetriggerlog.Debug("validate update", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("MessageQueueTrigger", err)
		return nil, err
	}
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// messagequeuetriggerlog.Debug("validate delete", zap.String("name", r.Name))
	return nil, nil
}
