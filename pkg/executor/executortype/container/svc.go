// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"
	"fmt"
	"maps"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

func (cn *Container) getSvPort(fn *fv1.Function) (port int32, err error) {
	if fn.Spec.PodSpec == nil {
		return port, fmt.Errorf("podspec is empty for function %s", fn.Name)
	}
	if len(fn.Spec.PodSpec.Containers) != 1 {
		return port, fmt.Errorf("podspec should have exactly one container %s", fn.Name)
	}
	if len(fn.Spec.PodSpec.Containers[0].Ports) != 1 {
		return port, fmt.Errorf("container should have exactly one port %s", fn.Name)
	}
	return fn.Spec.PodSpec.Containers[0].Ports[0].ContainerPort, nil
}

func (cn *Container) createOrGetSvc(ctx context.Context, fn *fv1.Function, deployLabels map[string]string, deployAnnotations map[string]string, svcName string, svcNamespace string) (*apiv1.Service, error) {
	targetPort, err := cn.getSvPort(fn)
	if err != nil {
		return nil, err
	}
	logger := otelUtils.LoggerWithTraceID(ctx, cn.logger)
	var ownerReferences []metav1.OwnerReference
	if cn.enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(fn, fv1.SchemeGroupVersion.WithKind("Function")),
		}
	}

	// The Service carries the managed-by label (RFC-0002) so the EndpointSlice
	// controller mirrors it onto the slices and the router's label-filtered
	// informer sees them. Labels only — the selector stays deployLabels.
	svcLabels := make(map[string]string, len(deployLabels)+1)
	maps.Copy(svcLabels, deployLabels)
	svcLabels[fv1.MANAGED_BY_LABEL] = fv1.MANAGED_BY_VALUE

	service := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            svcName,
			Labels:          svcLabels,
			Annotations:     deployAnnotations,
			OwnerReferences: ownerReferences,
		},
		Spec: apiv1.ServiceSpec{
			Ports: []apiv1.ServicePort{
				{
					Name:       "http-env",
					Port:       int32(80),
					TargetPort: intstr.FromInt(int(targetPort)),
				},
			},
			Selector: deployLabels,
			Type:     apiv1.ServiceTypeClusterIP,
		},
	}

	svc, created, err := util.CreateOrAdoptService(ctx, cn.kubernetesClient, logger, cn.instanceID, svcNamespace, service)
	if err != nil {
		return nil, err
	}
	if created {
		otelUtils.SpanTrackEvent(ctx, "svcCreated", otelUtils.GetAttributesForSvc(svc)...)
	}
	return svc, nil
}

func (cn *Container) deleteSvc(ctx context.Context, ns string, name string) error {
	return cn.kubernetesClient.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{})
}
