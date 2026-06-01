// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type HTTPTrigger struct {
	GenericWebhook[*v1.HTTPTrigger]
}

func (r *HTTPTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("httptrigger-resource")
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.HTTPTrigger{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-httptrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=httptriggers,verbs=create;update,versions=v1,name=mhttptrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &HTTPTrigger{}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-httptrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=httptriggers,verbs=create;update,versions=v1,name=vhttptrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &HTTPTrigger{}

func (r *HTTPTrigger) Validate(new *v1.HTTPTrigger) error {
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("HTTPTrigger", err)
	}
	return nil
}
