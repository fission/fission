// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Environment struct {
	GenericWebhook[*v1.Environment]
}

func (r *Environment) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("environment-resource")
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.Environment{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-environment,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=environments,verbs=create;update,versions=v1,name=menvironment.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Environment{}

// user: change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// Validation must cover UPDATE as well as CREATE: GHSA-wmgg-3p4h-48x7 noted
// that the prior CREATE-only marker let a tenant bypass admission by
// posting a clean Environment and then PATCHing in dangerous podspec
// fields like hostNetwork or privileged.
//+kubebuilder:webhook:path=/validate-fission-io-v1-environment,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=environments,verbs=create;update,versions=v1,name=venvironment.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Environment{}

func (r *Environment) Validate(new *v1.Environment) error {
	// Field-level validation (version range, pool size, enums) is enforced by
	// the API server via CEL (x-kubernetes-validations on the CRD). The webhook
	// retains only the podspec/container security rules CEL cannot express:
	// the executor and buildermgr ServiceAccounts schedule pods from these
	// runtime/builder podspecs and bare containers, so they must not set host
	// namespaces, hostPath, an alternate SA, privileged/allowPrivilegeEscalation,
	// or dangerous capabilities (GHSA-gx55-f84r-v3r7, GHSA-wmgg-3p4h-48x7,
	// GHSA-v455-mv2v-5g92, GHSA-m63v-2g9w-2w6v).
	errs := errors.Join(
		v1.ValidatePodSpecSafety("Environment.spec.runtime.podspec", new.Spec.Runtime.PodSpec),
		v1.ValidatePodSpecSafety("Environment.spec.builder.podspec", new.Spec.Builder.PodSpec),
		v1.ValidateContainerSafety("Environment.spec.runtime.container", new.Spec.Runtime.Container),
		v1.ValidateContainerSafety("Environment.spec.builder.container", new.Spec.Builder.Container),
	)
	// Non-security correctness invariant that CEL cannot express (it compares a
	// container's image to spec.runtime.image AND its name to the object's own
	// name): a runtime-podspec container that reuses the runtime image without
	// an explicit command must be named after the environment, or the pod will
	// not specialize. Preserved here from the former Environment.Validate path.
	if ps := new.Spec.Runtime.PodSpec; ps != nil {
		for _, c := range ps.Containers {
			if c.Command == nil && c.Image == new.Spec.Runtime.Image && c.Name != new.Name {
				errs = errors.Join(errs, fmt.Errorf("container with image same as runtime image in podspec must have name same as environment name (container %q, environment %q)", c.Name, new.Name))
			}
		}
	}
	if errs != nil {
		return v1.AggregateValidationErrors("Environment", errs)
	}
	return nil
}
