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
	"fmt"

	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Environment struct{}

// log is for logging in this package.
var environmentlog = loggerfactory.GetLogger().Named("environment-resource")

func (r *Environment) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.Environment{}).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-environment,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=environments,verbs=create;update,versions=v1,name=menvironment.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Environment{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (r *Environment) Default(_ context.Context, obj runtime.Object) error {
	return nil
}

// user: change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-environment,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=environments,verbs=create,versions=v1,name=venvironment.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Environment{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Environment) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	new, ok := obj.(*v1.Environment)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Environment but got a %T", obj))
	}
	environmentlog.Debug("validate create", zap.String("name", new.Name))
	return nil, r.validate(nil, new)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Environment) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	new, ok := newObj.(*v1.Environment)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Environment but got a %T", newObj))
	}
	environmentlog.Debug("validate update", zap.String("name", new.Name))
	return nil, r.validate(nil, new)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Environment) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *Environment) validate(_ *v1.Environment, new *v1.Environment) error {
	if err := new.Validate(); err != nil {
		err = v1.AggregateValidationErrors("Environment", err)
		return err
	}
	return nil
}
