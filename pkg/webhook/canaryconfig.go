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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

type CanaryConfig struct {
	GenericWebhook[*v1.CanaryConfig]
}

var canaryConfigLog = ctrl.Log.WithName("webhook").WithName("CanaryConfig")

func (r *CanaryConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = canaryConfigLog
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.CanaryConfig{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-canaryconfig,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=canaryconfigs,verbs=create;update,versions=v1,name=mcanaryconfig.fission.io,admissionReviewVersions=v1
// Refer Makefile -> generate-webhooks to generate config for manifests

var _ webhook.CustomDefaulter = &CanaryConfig{}

// user can change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// Validation webhooks can be added by adding tag: kubebuilder:webhook:path=/validate-fission-io-v1-canaryconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=canaryconfigs,verbs=create;update,versions=v1,name=vcanaryconfig.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &CanaryConfig{}
