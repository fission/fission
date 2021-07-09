/*
Copyright 2020 The Fission Authors.

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

package container

import (
	"context"

	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func (cn *Container) createOrGetSvc(fn *fv1.Function, deployLabels map[string]string, deployAnnotations map[string]string, svcName string, svcNamespace string) (*apiv1.Service, error) {
	service := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Labels:      deployLabels,
			Annotations: deployAnnotations,
		},
		Spec: apiv1.ServiceSpec{
			Ports: []apiv1.ServicePort{
				{
					Name:       "http-env",
					Port:       int32(80),
					TargetPort: intstr.FromInt(fn.Spec.Port),
				},
			},
			Selector: deployLabels,
			Type:     apiv1.ServiceTypeClusterIP,
		},
	}

	existingSvc, err := cn.kubernetesClient.CoreV1().Services(svcNamespace).Get(context.TODO(), svcName, metav1.GetOptions{})
	if err == nil {
		// to adopt orphan service
		if existingSvc.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != cn.instanceID {
			existingSvc.Annotations = service.Annotations
			existingSvc.Labels = service.Labels
			existingSvc.Spec.Ports = service.Spec.Ports
			existingSvc.Spec.Selector = service.Spec.Selector
			existingSvc.Spec.Type = service.Spec.Type
			existingSvc, err = cn.kubernetesClient.CoreV1().Services(svcNamespace).Update(context.TODO(), existingSvc, metav1.UpdateOptions{})
			if err != nil {
				cn.logger.Warn("error adopting service", zap.Error(err),
					zap.String("service", svcName), zap.String("ns", svcNamespace))
				return nil, err
			}
		}
		return existingSvc, err
	} else if k8s_err.IsNotFound(err) {
		svc, err := cn.kubernetesClient.CoreV1().Services(svcNamespace).Create(context.TODO(), service, metav1.CreateOptions{})
		if err != nil {
			if k8s_err.IsAlreadyExists(err) {
				svc, err = cn.kubernetesClient.CoreV1().Services(svcNamespace).Get(context.TODO(), svcName, metav1.GetOptions{})
			}
			if err != nil {
				return nil, err
			}
		}
		return svc, nil
	}
	return nil, err
}

func (cn *Container) deleteSvc(ns string, name string) error {
	return cn.kubernetesClient.CoreV1().Services(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
}