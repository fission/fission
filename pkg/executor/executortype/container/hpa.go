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

	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	asv1 "k8s.io/api/autoscaling/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const (
	DeploymentKind    = "Deployment"
	DeploymentVersion = "apps/v1"
)

func (cn *Container) createOrGetHpa(ctx context.Context, hpaName string, execStrategy *fv1.ExecutionStrategy,
	depl *appsv1.Deployment, deployLabels map[string]string, deployAnnotations map[string]string, ownerRefs []metav1.OwnerReference) (*asv1.HorizontalPodAutoscaler, error) {

	if depl == nil {
		return nil, errors.New("failed to create HPA, found empty deployment")
	}

	minRepl := int32(execStrategy.MinScale)
	if minRepl == 0 {
		minRepl = 1
	}
	maxRepl := int32(execStrategy.MaxScale)
	if maxRepl == 0 {
		maxRepl = minRepl
	}
	targetCPU := int32(execStrategy.TargetCPUPercent)

	hpa := &asv1.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:            hpaName,
			Labels:          deployLabels,
			Annotations:     deployAnnotations,
			OwnerReferences: ownerRefs,
		},
		Spec: asv1.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: asv1.CrossVersionObjectReference{
				Kind:       DeploymentKind,
				Name:       depl.ObjectMeta.Name,
				APIVersion: DeploymentVersion,
			},
			MinReplicas:                    &minRepl,
			MaxReplicas:                    maxRepl,
			TargetCPUUtilizationPercentage: &targetCPU,
		},
	}

	existingHpa, err := cn.getHpa(ctx, depl.ObjectMeta.Namespace, hpaName)
	if err == nil {
		// to adopt orphan service
		if existingHpa.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != cn.instanceID {
			existingHpa.Annotations = hpa.Annotations
			existingHpa.Labels = hpa.Labels
			existingHpa.Spec = hpa.Spec
			existingHpa, err = cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Update(ctx, existingHpa, metav1.UpdateOptions{})
			if err != nil {
				cn.logger.Warn("error adopting HPA", zap.Error(err),
					zap.String("HPA", hpaName), zap.String("ns", depl.ObjectMeta.Namespace))
				return nil, err
			}
		}
		return existingHpa, err
	} else if k8s_err.IsNotFound(err) {
		cHpa, err := cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Create(ctx, hpa, metav1.CreateOptions{})
		if err != nil {
			if k8s_err.IsAlreadyExists(err) {
				cHpa, err = cn.getHpa(ctx, depl.ObjectMeta.Namespace, hpaName)
			}
			if err != nil {
				return nil, err
			}
		}
		return cHpa, nil
	}
	return nil, err
}

func (cn *Container) getHpa(ctx context.Context, ns, name string) (*asv1.HorizontalPodAutoscaler, error) {
	return cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
}

func (cn *Container) updateHpa(ctx context.Context, hpa *asv1.HorizontalPodAutoscaler) error {
	_, err := cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(hpa.ObjectMeta.Namespace).Update(ctx, hpa, metav1.UpdateOptions{})
	return err
}

func (cn *Container) deleteHpa(ctx context.Context, ns string, name string) error {
	return cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(ns).Delete(ctx, name, metav1.DeleteOptions{})
}
