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
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// log is for logging in this package.
var functionlog = loggerfactory.GetLogger().Named("function-resource")

func (r *Function) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-function,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=mfunction.fission.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Function{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Function) Default() {
}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-function,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=vfunction.fission.io,admissionReviewVersions=v1

var _ webhook.Validator = &Function{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Function) ValidateCreate() error {
	functionlog.Debug("validate create", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		return AggregateValidationErrors("Function", err)
	}
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Function) ValidateUpdate(old runtime.Object) error {
	functionlog.Debug("validate update", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		return AggregateValidationErrors("Function", err)
	}
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Function) ValidateDelete() error {
	functionlog.Debug("validate delete", zap.String("name", r.Name))
	return nil
}
