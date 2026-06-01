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
	if err := new.Validate(); err != nil {
		err = v1.AggregateValidationErrors("Environment", err)
		return err
	}
	return nil
}
