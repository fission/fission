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
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type HTTPTrigger struct {
	GenericWebhook[*v1.HTTPTrigger]
}

// log is for logging in this package.
var httptriggerlog = loggerfactory.GetLogger().WithName("httptrigger-resource")

func (r *HTTPTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = httptriggerlog
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
