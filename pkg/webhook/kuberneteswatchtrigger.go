// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type KubernetesWatchTrigger struct {
	GenericWebhook[*v1.KubernetesWatchTrigger]
}

func (r *KubernetesWatchTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("kuberneteswatchtrigger-resource")
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.KubernetesWatchTrigger{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-kuberneteswatchtrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=kuberneteswatchtriggers,verbs=create;update,versions=v1,name=mkuberneteswatchtrigger.fission.io,admissionReviewVersions=v1

var _ admission.Defaulter[*v1.KubernetesWatchTrigger] = &KubernetesWatchTrigger{}

// user: change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-kuberneteswatchtrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=kuberneteswatchtriggers,verbs=create;update,versions=v1,name=vkuberneteswatchtrigger.fission.io,admissionReviewVersions=v1

var _ admission.Validator[*v1.KubernetesWatchTrigger] = &KubernetesWatchTrigger{}

func (r *KubernetesWatchTrigger) Validate(new *v1.KubernetesWatchTrigger) error {
	// Field rules (type enum, namespace DNS, function-reference) are enforced by
	// the API server via CEL; the webhook runs only the non-CEL checks
	// (label-selector qualified key/value) plus the cross-namespace check below.
	if err := new.ValidateForAdmission(); err != nil {
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
