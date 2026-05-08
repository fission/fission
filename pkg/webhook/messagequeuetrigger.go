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

	apiv1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type MessageQueueTrigger struct {
	GenericWebhook[*v1.MessageQueueTrigger]
}

// log is for logging in this package.
var messagequeuetriggerlog = loggerfactory.GetLogger().WithName("messagequeuetrigger-resource")

func (r *MessageQueueTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = messagequeuetriggerlog
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.MessageQueueTrigger{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-messagequeuetrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=mmessagequeuetrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &MessageQueueTrigger{}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-messagequeuetrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=messagequeuetriggers,verbs=create;update,versions=v1,name=vmessagequeuetrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &MessageQueueTrigger{}

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
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("MessageQueueTrigger", err)
	}
	return nil
}

// validateAllowedPodSpec rejects user-supplied PodSpec fields that the
// MessageQueueTrigger connector controller refuses to honour. The set of
// disallowed fields here MUST stay in lock-step with
// pkg/executor/util.MergeAllowedPodSpecFields.
func validateAllowedPodSpec(ps *apiv1.PodSpec) error {
	var bad []string
	for _, c := range ps.Containers {
		if c.Image != "" {
			bad = append(bad, "podSpec.containers[].image")
		}
		if len(c.Command) > 0 {
			bad = append(bad, "podSpec.containers[].command")
		}
		if len(c.Args) > 0 {
			bad = append(bad, "podSpec.containers[].args")
		}
		if len(c.Env) > 0 {
			bad = append(bad, "podSpec.containers[].env")
		}
		if len(c.VolumeMounts) > 0 {
			bad = append(bad, "podSpec.containers[].volumeMounts")
		}
	}
	if len(ps.Volumes) > 0 {
		bad = append(bad, "podSpec.volumes")
	}
	if ps.ServiceAccountName != "" {
		bad = append(bad, "podSpec.serviceAccountName")
	}
	if ps.HostNetwork {
		bad = append(bad, "podSpec.hostNetwork")
	}
	if ps.HostPID {
		bad = append(bad, "podSpec.hostPID")
	}
	if ps.HostIPC {
		bad = append(bad, "podSpec.hostIPC")
	}
	if ps.SecurityContext != nil && ps.SecurityContext.RunAsUser != nil {
		bad = append(bad, "podSpec.securityContext.runAsUser")
	}
	if len(bad) > 0 {
		return fmt.Errorf("MessageQueueTrigger.spec.podSpec contains disallowed fields: %v "+
			"(allowlist: nodeSelector, tolerations, affinity, runtimeClassName, containers[].resources)", bad)
	}
	return nil
}
