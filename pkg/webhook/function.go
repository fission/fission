// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Function struct {
	GenericWebhook[*v1.Function]
}

func (r *Function) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("function-resource")
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.Function{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-function,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=mfunction.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Function{}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-function,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=vfunction.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Function{}

func (r *Function) Validate(new *v1.Function) error {
	for _, cnfMap := range new.Spec.ConfigMaps {
		if cnfMap.Namespace != new.Namespace {
			err := fmt.Errorf("configMap's [%s] and function's Namespace [%s] are different. ConfigMap needs to be present in the same namespace as function", cnfMap.Namespace, new.Namespace)
			return v1.AggregateValidationErrors("Function", err)
		}
	}
	for _, secret := range new.Spec.Secrets {
		if secret.Namespace != new.Namespace {
			err := fmt.Errorf("secret  [%s] and function's Namespace [%s] are different. Secret needs to be present in the same namespace as function", secret.Namespace, new.Namespace)
			return v1.AggregateValidationErrors("Function", err)
		}
	}
	// Cross-namespace EnvironmentRef closes GHSA-cvw6-gfvv-953q. An empty
	// namespace remains accepted — the Fission CLI populates it with the
	// function's own namespace at creation time (pkg/fission-cli/cmd/function/
	// create.go), and downstream controllers tolerate empty via
	// DefaultNSResolver. Rejecting only the explicit cross-namespace value is
	// sufficient for this advisory; defaulting an empty namespace at admission
	// is a separate hardening track tracked outside this fix.
	if envRef := new.Spec.Environment; envRef.Namespace != "" && envRef.Namespace != new.Namespace {
		err := fmt.Errorf("environment's namespace [%s] and function's namespace [%s] are different; cross-namespace Environment reference is not allowed",
			envRef.Namespace, new.Namespace)
		return v1.AggregateValidationErrors("Function", err)
	}
	// Cross-namespace PackageRef closes GHSA-3r8v-2xmj-5c39. Same shape as
	// the EnvironmentRef check above, including the empty-is-accepted rule.
	if pkgRef := new.Spec.Package.PackageRef; pkgRef.Namespace != "" && pkgRef.Namespace != new.Namespace {
		err := fmt.Errorf("package's namespace [%s] and function's namespace [%s] are different; cross-namespace Package reference is not allowed",
			pkgRef.Namespace, new.Namespace)
		return v1.AggregateValidationErrors("Function", err)
	}

	// Field-level validation (executor enums, scale bounds, DNS names) is
	// enforced by the API server via CEL (x-kubernetes-validations on the CRD).
	// The webhook retains only what CEL cannot express: the cross-namespace
	// reference checks above, plus the podspec security rules below — the
	// executor ServiceAccount can create pods, so a tenant-supplied podspec
	// must not set host namespaces, hostPath, an alternate SA, privileged
	// containers, etc. (GHSA-v455-mv2v-5g92).
	if err := v1.ValidatePodSpecSafety("Function.spec.podspec", new.Spec.PodSpec); err != nil {
		return v1.AggregateValidationErrors("Function", err)
	}
	return nil
}
