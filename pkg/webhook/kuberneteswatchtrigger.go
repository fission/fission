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

type KubernetesWatchTrigger struct {
	webhook *WebhookTemplate[*v1.KubernetesWatchTrigger]
}

func NewKubernetesWatchTrigger() *KubernetesWatchTrigger {
	logger := loggerfactory.GetLogger().Named("kuberneteswatchtrigger-resource")
	return &KubernetesWatchTrigger{
		webhook: NewWebhookTemplate(logger, nil, validateKubernetesWatchTrigger),
	}
}

func (r *KubernetesWatchTrigger) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return r.webhook.SetupWebhookWithManager(mgr, &v1.KubernetesWatchTrigger{})
}

func validateKubernetesWatchTrigger(old, new *v1.KubernetesWatchTrigger) error {
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("Watch", err)
	}
	return nil
}
