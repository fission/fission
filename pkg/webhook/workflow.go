// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"fmt"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Workflow struct {
	GenericWebhook[*v1.Workflow]
}

func (r *Workflow) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("workflow-resource")
	r.Validator = r
	r.Defaulter = r
	r.Warner = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.Workflow{})
}

//+kubebuilder:webhook:path=/mutate-fission-io-v1-workflow,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=workflows,verbs=create;update,versions=v1,name=mworkflow.fission.io,admissionReviewVersions=v1

var _ admission.Defaulter[*v1.Workflow] = &Workflow{}

// ApplyDefaults fills the function-reference type on Task states ("type
// defaults to name" — the RFC's worked example applies verbatim via kubectl).
func (r *Workflow) ApplyDefaults(new *v1.Workflow) error {
	new.Spec.ApplyDefaults()
	return nil
}

//+kubebuilder:webhook:path=/validate-fission-io-v1-workflow,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=workflows,verbs=create;update,versions=v1,name=vworkflow.fission.io,admissionReviewVersions=v1

var _ admission.Validator[*v1.Workflow] = &Workflow{}

func (r *Workflow) Validate(new *v1.Workflow) error {
	// The whole workflow rule set is non-CEL (graph reachability needs a
	// traversal, JSONPath needs a parser), so admission enforces Validate in
	// full. Referenced-function existence is deliberately NOT checked here:
	// GitOps applies resources in arbitrary order, so a dangling reference is
	// a status condition for the phase-2 controller, not an admission error.
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("Workflow", err)
	}
	return nil
}

// workflowBuiltinErrorTypes is the set of built-in error classes a Catch
// route can rely on; anything else Fission.*-prefixed earns a warning.
var workflowBuiltinErrorTypes = func() map[string]bool {
	m := make(map[string]bool, len(v1.WorkflowBuiltinErrorTypes))
	for _, e := range v1.WorkflowBuiltinErrorTypes {
		m[e] = true
	}
	return m
}()

// Warnings flags accepted-but-suspect specs: a Catch route on a typo'd
// built-in error class (e.g. "Fission.Timout") passes validation — errorType
// is free-form because functions emit arbitrary typed errors — but never
// matches at runtime, silently disabling the route the author wrote.
func (r *Workflow) Warnings(new *v1.Workflow) admission.Warnings {
	var warnings admission.Warnings
	for name, st := range new.Spec.States {
		for _, c := range st.Catch {
			if strings.HasPrefix(c.ErrorType, "Fission.") && !workflowBuiltinErrorTypes[c.ErrorType] {
				warnings = append(warnings, fmt.Sprintf(
					"state %q catches %q, which is not a built-in Fission error class — the route will only match a function-emitted error of that exact type",
					name, c.ErrorType))
			}
		}
	}
	return warnings
}
