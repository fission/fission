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
package cms

import (
	"context"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// getConfigmapRelatedFuncs returns functions related to configmap in the same namespace
func getConfigmapRelatedFuncs(ctx context.Context, m *metav1.ObjectMeta, fissionClient versioned.Interface) ([]fv1.Function, error) {
	funcList, err := fissionClient.CoreV1().Functions(m.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// In future a cache that populates at start and is updated on changes might be better solution
	relatedFunctions := make([]fv1.Function, 0)
	for _, f := range funcList.Items {
		for _, cm := range f.Spec.ConfigMaps {
			if (cm.Name == m.Name) && (cm.Namespace == m.Namespace) {
				relatedFunctions = append(relatedFunctions, f)
				break
			}
		}
	}
	return relatedFunctions, nil
}

func ConfigMapEventHandlers(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface, types map[fv1.ExecutorType]executortype.ExecutorType) k8sCache.ResourceEventHandlerFuncs {

	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) {},
		DeleteFunc: func(obj any) {},
		UpdateFunc: func(oldObj any, newObj any) {
			oldCm := oldObj.(*apiv1.ConfigMap)
			newCm := newObj.(*apiv1.ConfigMap)
			if oldCm.ResourceVersion != newCm.ResourceVersion {
				funcs, err := getConfigmapRelatedFuncs(ctx, &newCm.ObjectMeta, fissionClient)
				if err != nil {
					logger.Error(err, "Failed to get functions related to configmap", "configmap_name", newCm.Name, "configmap_namespace", newCm.Namespace)
				}

				if len(funcs) == 0 {
					return
				}

				logger.V(1).Info("Configmap changed",
					"configmap_name", newCm.Name,
					"configmap_namespace", newCm.Namespace)
				refreshPods(ctx, logger, funcs, types)
			}
		},
	}
}
