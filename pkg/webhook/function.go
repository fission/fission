// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Function struct {
	GenericWebhook[*v1.Function]
	// reader is the uncached API reader used to fetch the referenced
	// Environment when validating RFC-0023 state opt-in (get-only, so no cache
	// warm-up or list/watch RBAC needed).
	reader client.Reader
}

func (r *Function) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("function-resource")
	r.Validator = r
	r.Warner = r
	r.reader = mgr.GetAPIReader()
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.Function{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-function,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=mfunction.fission.io,admissionReviewVersions=v1

var _ admission.Defaulter[*v1.Function] = &Function{}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-function,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functions,verbs=create;update,versions=v1,name=vfunction.fission.io,admissionReviewVersions=v1

var _ admission.Validator[*v1.Function] = &Function{}

// Warnings flags accepted-but-suspect specs. The concurrency-enforcement
// annotation FAILS OPEN — any value other than "strict" silently means
// router-local (approximate) accounting, the opposite of what a user typing
// "Strict" or "STRICT" asked for — so a typo earns a warning at admission,
// where the user is still looking.
func (r *Function) Warnings(new *v1.Function) admission.Warnings {
	if v, ok := new.Annotations[v1.ConcurrencyEnforcementAnnotation]; ok && v != v1.ConcurrencyEnforcementStrict {
		return admission.Warnings{fmt.Sprintf(
			"annotation %s has unrecognized value %q; only %q is recognized — the function will use router-local (approximate) concurrency accounting",
			v1.ConcurrencyEnforcementAnnotation, v, v1.ConcurrencyEnforcementStrict)}
	}
	return nil
}

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

	// RFC-0023 state opt-in is incompatible with an AllowedFunctionsPerContainer:
	// infinite environment: those pods serve many functions from ONE shared
	// mount, and the scoped state token is written to a single per-pod file —
	// a second function specializing onto the pod would overwrite the first's
	// token, breaking cross-function scope isolation (S1). CEL cannot express
	// this (it needs the referenced Environment), so it lives here. Fails open
	// on a lookup miss (env not yet created / transiently unreadable): the
	// function-agnostic checks already passed, and a confirmed-Infinite env is
	// the only rejection.
	if new.Spec.State != nil {
		if err := r.rejectStateOnInfiniteEnv(new); err != nil {
			return v1.AggregateValidationErrors("Function", err)
		}
	}

	// Field rules (executor enums, scale bounds, reference-name DNS, etc.) are
	// enforced by the API server via CEL; the webhook runs only the non-CEL
	// checks (pod-spec security) plus the cross-namespace checks above.
	if err := new.ValidateForAdmission(); err != nil {
		return v1.AggregateValidationErrors("Function", err)
	}
	return nil
}

func (r *Function) rejectStateOnInfiniteEnv(fn *v1.Function) error {
	if r.reader == nil {
		return nil
	}
	envNS := fn.Spec.Environment.Namespace
	if envNS == "" {
		envNS = fn.Namespace
	}
	var env v1.Environment
	if err := r.reader.Get(context.Background(), types.NamespacedName{Name: fn.Spec.Environment.Name, Namespace: envNS}, &env); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // env not created yet; the executor rechecks at specialize time
		}
		return nil // transient lookup failure must not block unrelated function writes
	}
	if env.Spec.AllowedFunctionsPerContainer == v1.AllowedFunctionsPerContainerInfinite {
		return fmt.Errorf("the state API (spec.state) is incompatible with environment %q (allowedFunctionsPerContainer: infinite): its pods serve multiple functions from one shared mount, which cannot deliver a per-function scoped state token", env.Name)
	}
	return nil
}
