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

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type TimeTrigger struct {
	webhook *WebhookTemplate[*v1.TimeTrigger]
}

func NewTimeTrigger() *TimeTrigger {
	logger := loggerfactory.GetLogger().Named("timetrigger-resource")
	return &TimeTrigger{
		webhook: NewWebhookTemplate(logger, nil, validateTimeTrigger),
	}
}

func (r *TimeTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return r.webhook.SetupWebhookWithManager(mgr, &v1.TimeTrigger{})
}

func validateTimeTrigger(old, new *v1.TimeTrigger) error {
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("TimeTrigger", err)
	}
	return nil
}
