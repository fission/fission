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

type Function struct{}

// log is for logging in this package.
var functionlog = loggerfactory.GetLogger().Named("function-resource")

func (r *Function) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.Function{}).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-function,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=mfunction.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Function{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (r *Function) Default(_ context.Context, obj runtime.Object) error {
	return nil
}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-function,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=vfunction.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Function{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Function) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	new, ok := obj.(*v1.Function)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Function but got a %T", obj))
	}
	functionlog.Debug("validate create", zap.String("name", new.Name))
	return nil, r.validate(nil, new)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Function) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	new, ok := newObj.(*v1.Function)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a Function but got a %T", newObj))
	}
	functionlog.Debug("validate update", zap.String("name", new.Name))
	return nil, r.validate(nil, new)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (r *Function) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *Function) validate(old *v1.Function, new *v1.Function) error {
	for _, cnfMap := range new.Spec.ConfigMaps {
		if cnfMap.Namespace != new.Namespace {
			err := fmt.Errorf("ConfigMap's [%s] and function's Namespace [%s] are different. ConfigMap needs to be present in the same namespace as function", cnfMap.Namespace, new.Namespace)
			return v1.AggregateValidationErrors("Function", err)
		}
	}
	for _, secret := range new.Spec.Secrets {
		if secret.Namespace != new.Namespace {
			err := fmt.Errorf("secret  [%s] and function's Namespace [%s] are different. Secret needs to be present in the same namespace as function", secret.Namespace, new.Namespace)
			return v1.AggregateValidationErrors("Function", err)
		}
	}

	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("Function", err)
	}
	return nil
}
