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

type CanaryConfig struct {
	GenericWebhook[*v1.CanaryConfig]
}

func (r *CanaryConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("canaryconfig-resource")
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.CanaryConfig{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-canaryconfig,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=canaryconfigs,verbs=create;update,versions=v1,name=mcanaryconfig.fission.io,admissionReviewVersions=v1
// Refer Makefile -> generate-webhooks to generate config for manifests

var _ webhook.CustomDefaulter = &CanaryConfig{}

// user can change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// Validation webhooks can be added by adding tag: kubebuilder:webhook:path=/validate-fission-io-v1-canaryconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=canaryconfigs,verbs=create;update,versions=v1,name=vcanaryconfig.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &CanaryConfig{}
