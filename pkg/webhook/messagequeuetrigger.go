// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"fmt"

	apiv1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	executorutil "github.com/fission/fission/pkg/executor/util"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type MessageQueueTrigger struct {
	GenericWebhook[*v1.MessageQueueTrigger]
}

func (r *MessageQueueTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("messagequeuetrigger-resource")
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.MessageQueueTrigger{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-messagequeuetrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=mmessagequeuetrigger.fission.io,admissionReviewVersions=v1

var _ admission.Defaulter[*v1.MessageQueueTrigger] = &MessageQueueTrigger{}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-messagequeuetrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=vmessagequeuetrigger.fission.io,admissionReviewVersions=v1

var _ admission.Validator[*v1.MessageQueueTrigger] = &MessageQueueTrigger{}

func (r *MessageQueueTrigger) Validate(new *v1.MessageQueueTrigger) error {
	// SECURITY: reject MessageQueueTrigger.Spec.PodSpec fields that are
	// not on the controller-side allowlist. Without this admission-time
	// check a tenant with create/update on MessageQueueTrigger could
	// otherwise overwrite the connector container's image, command,
	// args, env, mounts, ServiceAccount, host namespaces, or runAsUser
	// — see GHSA-7m8x-qg2j-4m3v. The controller still drops these
	// fields server-side as defence in depth.
	if new.Spec.PodSpec != nil {
		if err := validateAllowedPodSpec(new.Spec.PodSpec); err != nil {
			return err
		}
	}
	// Field rules (function-reference shape) are enforced by the API server via
	// CEL; the webhook runs only the non-CEL checks: the podspec allowlist above
	// and message-queue type/topic validity (validator registry) below.
	if err := new.ValidateForAdmission(); err != nil {
		return v1.AggregateValidationErrors("MessageQueueTrigger", err)
	}
	return nil
}

// validateAllowedPodSpec rejects user-supplied PodSpec fields that the
// MessageQueueTrigger connector controller refuses to honour. The single
// source of truth for the disallow list is
// executorutil.DisallowedPodSpecFields — extending the allowlist here
// (without touching the executor) would silently let admission accept
// fields that the controller still drops.
func validateAllowedPodSpec(ps *apiv1.PodSpec) error {
	bad := executorutil.DisallowedPodSpecFields(ps)
	if len(bad) == 0 {
		return nil
	}
	prefixed := make([]string, len(bad))
	for i, name := range bad {
		prefixed[i] = "spec.podspec." + name
	}
	return fmt.Errorf("MessageQueueTrigger.spec.podspec contains disallowed fields: %v "+
		"(allowlist: nodeSelector, tolerations, affinity, runtimeClassName, containers[].resources)", prefixed)
}
