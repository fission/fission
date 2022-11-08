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
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var messagequeuetriggerlog = logf.Log.WithName("messagequeuetrigger-resource")

func (r *MessageQueueTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-fission-io-v1-messagequeuetrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=mmessagequeuetrigger.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &MessageQueueTrigger{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *MessageQueueTrigger) Default() {
	messagequeuetriggerlog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-messagequeuetrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=vmessagequeuetrigger.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &MessageQueueTrigger{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateCreate() error {
	messagequeuetriggerlog.Info("validate create", "name", r.Name)
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("MessageQueueTrigger", err)
		return err
	}
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateUpdate(old runtime.Object) error {
	messagequeuetriggerlog.Info("validate update", "name", r.Name)
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("MessageQueueTrigger", err)
		return err
	}
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *MessageQueueTrigger) ValidateDelete() error {
	messagequeuetriggerlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil
}
