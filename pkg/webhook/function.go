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
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type Function struct {
	webhook *WebhookTemplate[*v1.Function]
}

func NewFunction() *Function {
	logger := loggerfactory.GetLogger().Named("function-resource")
	return &Function{
		webhook: NewWebhookTemplate(logger, nil, validateFunction),
	}
}

func (r *Function) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return r.webhook.SetupWebhookWithManager(mgr, &v1.Function{})
}

func validateFunction(old, new *v1.Function) error {
	for _, cnfMap := range new.Spec.ConfigMaps {
		if cnfMap.Namespace != new.Namespace {
			err := fmt.Errorf("ConfigMap's [%s] and function's Namespace [%s] are different. ConfigMap needs to be present in the same namespace as function", cnfMap.Namespace, new.Namespace)
			return v1.AggregateValidationErrors("Function", err)
		}
	}
	for _, secret := range new.Spec.Secrets {
		if secret.Namespace != new.Namespace {
			err := fmt.Errorf("secret  [%s] and function's Namespace [%s] are different. Secret needs to be present in the same namespace as function", secret.Namespace, new.Namespace)
			return v1.AggregateValidationErrors("Function", err)
		}
	}

	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("Function", err)
	}
	return nil
}
