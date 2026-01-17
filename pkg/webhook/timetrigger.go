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

type TimeTrigger struct {
	GenericWebhook[*v1.TimeTrigger]
}

// log is for logging in this package.
var timetriggerlog = loggerfactory.GetLogger().WithName("timetrigger-resource")

func (r *TimeTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = timetriggerlog
	r.Validator = r
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.TimeTrigger{})
}

// Admission webhooks can be added by adding tag: kubebuilder:webhook:path=/mutate-fission-io-v1-timetrigger,mutating=true,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=timetriggers,verbs=create;update,versions=v1,name=mtimetrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &TimeTrigger{}

// user change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-fission-io-v1-timetrigger,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=timetriggers,verbs=create;update,versions=v1,name=vtimetrigger.fission.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &TimeTrigger{}

func (r *TimeTrigger) Validate(new *v1.TimeTrigger) error {
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("TimeTrigger", err)
	}

	return nil
}
