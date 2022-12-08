/*
Copyright 2018 The Fission Authors.

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

package poolmgr

import (
	"context"

	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

func getIstioServiceLabels(fnName string) map[string]string {
	return map[string]string{
		"functionName": fnName,
	}
}

// FunctionEventHandlers provides handlers for function resource events.
// Based on function create/update/delete event, we create role binding
// for the secret/configmap access which is used by fetcher component.
// If istio is enabled, we create a service for the function.
func FunctionEventHandlers(ctx context.Context, logger *zap.Logger, kubernetesClient kubernetes.Interface, fissionfnNamespace string, istioEnabled bool) k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			fn := obj.(*fv1.Function)

			// Since istio only allows accessing pod through k8s service,
			// for the functions with executor type "poolmgr" we need to
			// create a service for sending requests to pod in pool.
			// Functions with executor type "Newdeploy" is specialized at
			// pod starts. In this case, just ignore such functions.
			fnExecutorType := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType

			// In some cases, user may not enter the executorType explicitly, for example in his spec.yaml.
			// we assume it to be of type poolmgr
			if fnExecutorType != "" && fnExecutorType != fv1.ExecutorTypePoolmgr {
				return
			}

			// create or update role-binding
			envNs := fissionfnNamespace
			if fn.Spec.Environment.Namespace != metav1.NamespaceDefault {
				envNs = fn.Spec.Environment.Namespace
			}

			if istioEnabled {
				// create a same name service for function
				// since istio only allows the traffic to service
				sel := map[string]string{
					"functionName": fn.ObjectMeta.Name,
					"functionUid":  string(fn.ObjectMeta.UID),
				}

				svcName := utils.GetFunctionIstioServiceName(fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)

				// service for accepting user traffic
				svc := apiv1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: envNs,
						Name:      svcName,
						Labels:    getIstioServiceLabels(fn.ObjectMeta.Name),
					},
					Spec: apiv1.ServiceSpec{
						Type: apiv1.ServiceTypeClusterIP,
						Ports: []apiv1.ServicePort{
							// Service port name should begin with a recognized prefix, or the traffic will be
							// treated as TCP traffic. (https://istio.io/docs/setup/kubernetes/additional-setup/requirements/)
							{
								Name:       "http-fetcher",
								Protocol:   apiv1.ProtocolTCP,
								Port:       8000,
								TargetPort: intstr.FromInt(8000),
							},
							{
								Name:       "http-env",
								Protocol:   apiv1.ProtocolTCP,
								Port:       8888,
								TargetPort: intstr.FromInt(8888),
							},
						},
						Selector: sel,
					},
				}

				// create function istio service if it does not exist
				_, err := kubernetesClient.CoreV1().Services(envNs).Create(ctx, &svc, metav1.CreateOptions{})
				if err != nil && !kerrors.IsAlreadyExists(err) {
					logger.Error("error creating istio service for function",
						zap.Error(err),
						zap.String("service_name", svcName),
						zap.String("function_name", fn.ObjectMeta.Name),
						zap.Any("selectors", sel))
				}
			}
		},

		DeleteFunc: func(obj interface{}) {
			fn := obj.(*fv1.Function)

			fnExecutorType := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
			if fnExecutorType != "" && fnExecutorType != fv1.ExecutorTypePoolmgr {
				return
			}

			envNs := fissionfnNamespace
			if fn.Spec.Environment.Namespace != metav1.NamespaceDefault {
				envNs = fn.Spec.Environment.Namespace
			}

			if istioEnabled {
				svcName := utils.GetFunctionIstioServiceName(fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
				// delete function istio service
				err := kubernetesClient.CoreV1().Services(envNs).Delete(ctx, svcName, metav1.DeleteOptions{})
				if err != nil && !kerrors.IsNotFound(err) {
					logger.Error("error deleting istio service for function",
						zap.Error(err),
						zap.String("service_name", svcName),
						zap.String("function_name", fn.ObjectMeta.Name))

				}
			}
		},
	}

}
