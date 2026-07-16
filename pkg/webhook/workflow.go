// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
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
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.Workflow{})
}

//+kubebuilder:webhook:path=/validate-fission-io-v1-workflow,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=workflows,verbs=create;update,versions=v1,name=vworkflow.fission.io,admissionReviewVersions=v1

var _ admission.Validator[*v1.Workflow] = &Workflow{}

func (r *Workflow) Validate(new *v1.Workflow) error {
	// The whole workflow rule set is non-CEL (graph reachability needs a
	// traversal, JSONPath needs a parser), so admission enforces everything.
	// Referenced-function existence is deliberately NOT checked here: GitOps
	// applies resources in arbitrary order, so a dangling reference is a
	// status condition for the phase-2 controller, not an admission error.
	if err := new.ValidateForAdmission(); err != nil {
		return v1.AggregateValidationErrors("Workflow", err)
	}
	return nil
}
