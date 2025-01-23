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
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type KubernetesWatchTrigger struct{}

// log is for logging in this package.
var kuberneteswatchtriggerlog = loggerfactory.GetLogger().Named("kuberneteswatchtrigger-resource")

func (r *KubernetesWatchTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.KubernetesWatchTrigger{}).
		Complete()
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-kuberneteswatchtrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=kuberneteswatchtriggers,verbs=create;update,versions=v1,name=mkuberneteswatchtrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &KubernetesWatchTrigger{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *KubernetesWatchTrigger) Default() {
	kuberneteswatchtriggerlog.Debug("default", zap.String("name", r.Name))
}

// user: change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-kuberneteswatchtrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=kuberneteswatchtriggers,verbs=create,versions=v1,name=vkuberneteswatchtrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &KubernetesWatchTrigger{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *KubernetesWatchTrigger) ValidateCreate() (admission.Warnings, error) {
	kuberneteswatchtriggerlog.Debug("validate create", zap.String("name", r.Name))
	err := r.Validate()
	if err != nil {
		err = AggregateValidationErrors("Watch", err)
		return nil, err
	}
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *KubernetesWatchTrigger) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	// WATCH UPDATE NOT IMPLEMENTED
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *KubernetesWatchTrigger) ValidateDelete() (admission.Warnings, error) {
	kuberneteswatchtriggerlog.Debug("validate delete", zap.String("name", r.Name))
	return nil, nil
}
