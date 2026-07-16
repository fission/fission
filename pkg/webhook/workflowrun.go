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

type WorkflowRun struct {
	GenericWebhook[*v1.WorkflowRun]
}

func (r *WorkflowRun) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("workflowrun-resource")
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.WorkflowRun{})
}

//+kubebuilder:webhook:path=/validate-fission-io-v1-workflowrun,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=workflowruns,verbs=create;update,versions=v1,name=vworkflowrun.fission.io,admissionReviewVersions=v1

var _ admission.Validator[*v1.WorkflowRun] = &WorkflowRun{}

func (r *WorkflowRun) Validate(new *v1.WorkflowRun) error {
	// The input byte cap is the non-CEL rule here (raw-bytes fields break CEL
	// cost estimation); workflowRef existence is a controller concern, same
	// GitOps-ordering reasoning as the Workflow webhook.
	if err := new.ValidateForAdmission(); err != nil {
		return v1.AggregateValidationErrors("WorkflowRun", err)
	}
	return nil
}
