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
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var messagequeuetriggerlog = loggerfactory.GetLogger().Named("messagequeuetrigger-resource")

func (r *MessageQueueTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-messagequeuetrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=mmessagequeuetrigger.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &MessageQueueTrigger{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *MessageQueueTrigger) Default() {
	messagequeuetriggerlog.Debug("default", zap.String("name", r.Name))
}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-messagequeuetrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=vmessagequeuetrigger.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &MessageQueueTrigger{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateCreate() error {
	messagequeuetriggerlog.Debug("validate create", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("MessageQueueTrigger", err)
		return err
	}
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateUpdate(old runtime.Object) error {
	messagequeuetriggerlog.Debug("validate update", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("MessageQueueTrigger", err)
		return err
	}
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateDelete() error {
	messagequeuetriggerlog.Debug("validate delete", zap.String("name", r.Name))
	return nil
}
