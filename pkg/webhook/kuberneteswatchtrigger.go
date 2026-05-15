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
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type KubernetesWatchTrigger struct {
	GenericWebhook[*v1.KubernetesWatchTrigger]
}

// log is for logging in this package.
var kuberneteswatchtriggerlog = loggerfactory.GetLogger().WithName("kuberneteswatchtrigger-resource")

func (r *KubernetesWatchTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = kuberneteswatchtriggerlog
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.KubernetesWatchTrigger{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-kuberneteswatchtrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=kuberneteswatchtriggers,verbs=create;update,versions=v1,name=mkuberneteswatchtrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &KubernetesWatchTrigger{}

// user: change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-kuberneteswatchtrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=kuberneteswatchtriggers,verbs=create;update,versions=v1,name=vkuberneteswatchtrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &KubernetesWatchTrigger{}

func (r *KubernetesWatchTrigger) Validate(new *v1.KubernetesWatchTrigger) error {
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("Watch", err)
	}

	// Refuse cross-namespace Watch targets — the kubewatcher controller has
	// cluster-wide watch privileges and would otherwise stream every event
	// from the referenced namespace to the trigger's function, defeating
	// namespace-as-tenant boundaries (GHSA-gc3j-79f2-7vvw).
	if new.Spec.Namespace != "" && new.Spec.Namespace != new.Namespace {
		return ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("KubernetesWatchTrigger.spec.namespace must equal the trigger namespace (got spec.namespace=%q, metadata.namespace=%q)",
				new.Spec.Namespace, new.Namespace))
	}
	return nil
}
