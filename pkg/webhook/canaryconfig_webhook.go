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

type CanaryConfig struct{}

// log is for logging in this package.
var canaryconfiglog = loggerfactory.GetLogger().Named("canaryconfig-resource")

func (r *CanaryConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.CanaryConfig{}).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-canaryconfig,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=canaryconfigs,verbs=create;update,versions=v1,name=mcanaryconfig.fission.io,admissionReviewVersions=v1
// Refer Makefile -> generate-webhooks to generate config for manifests

var _ webhook.CustomDefaulter = &CanaryConfig{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *CanaryConfig) Default(_ context.Context, obj runtime.Object) error {
	// canaryconfiglog.Debug("default", zap.String("name", r.Name))
}

// user can change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// Validation webhooks can be added by adding tag: kubebuilder:webhook:path=/validate-fission-io-v1-canaryconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=canaryconfigs,verbs=create;update,versions=v1,name=vcanaryconfig.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &CanaryConfig{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *CanaryConfig) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	// canaryconfiglog.Debug("validate create", zap.String("name", r.Name))
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *CanaryConfig) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	// canaryconfiglog.Debug("validate update", zap.String("name", r.Name))
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *CanaryConfig) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// canaryconfiglog.Debug("validate delete", zap.String("name", r.Name))
	return nil, nil
}
