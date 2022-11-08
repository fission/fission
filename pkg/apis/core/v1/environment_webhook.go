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
var environmentlog = loggerfactory.GetLogger().Named("environment-resource")

func (r *Environment) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-environment,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=environments,verbs=create;update,versions=v1,name=menvironment.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Environment{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Environment) Default() {
	environmentlog.Debug("default", zap.String("name", r.Name))
}

// user: change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-environment,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=environments,verbs=create;update,versions=v1,name=venvironment.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Environment{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Environment) ValidateCreate() error {
	environmentlog.Debug("validate create", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("Environment", err)
		return err
	}
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Environment) ValidateUpdate(old runtime.Object) error {
	environmentlog.Debug("validate update", zap.String("name", r.Name))

	// TODO(user): fill in your validation logic upon object update.
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Environment) ValidateDelete() error {
	environmentlog.Debug("validate delete", zap.String("name", r.Name))
	return nil
}
