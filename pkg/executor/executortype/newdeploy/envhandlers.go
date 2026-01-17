/*
Copyright 2021 The Fission Authors.

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
package newdeploy

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func (deploy *NewDeploy) EnvEventHandlers(ctx context.Context) k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) {},
		DeleteFunc: func(obj any) {},
		UpdateFunc: func(oldObj any, newObj any) {
			newEnv := newObj.(*fv1.Environment)
			oldEnv := oldObj.(*fv1.Environment)
			// Currently only an image update in environment calls for function's deployment recreation. In future there might be more attributes which would want to do it
			if oldEnv.Spec.Runtime.Image != newEnv.Spec.Runtime.Image {
				deploy.logger.V(1).Info("Updating all function of the environment that changed, old env:", "environment", oldEnv)
				funcs := deploy.getEnvFunctions(ctx, &newEnv.ObjectMeta)
				for _, f := range funcs {
					function, err := deploy.fissionClient.CoreV1().Functions(f.ObjectMeta.Namespace).Get(ctx, f.Name, metav1.GetOptions{})
					if err != nil {
						deploy.logger.Error(nil, "Error getting function", "function", function)
						continue
					}
					err = deploy.updateFuncDeployment(ctx, function, newEnv)
					if err != nil {
						deploy.logger.Error(nil, "Error updating function", "function", function)
						continue
					}
				}
			}
		},
	}
}
