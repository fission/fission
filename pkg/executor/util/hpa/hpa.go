/*
Copyright 2022 The Fission Authors.

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
package hpa

import (
	"context"
	"errors"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	asv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// Deployment Constants
const (
	DeploymentKind    = "Deployment"
	DeploymentVersion = "apps/v1"
)

type HpaOperations struct {
	logger           *zap.Logger
	kubernetesClient kubernetes.Interface
	instanceID       string
}

func NewHpaOperations(logger *zap.Logger, kubernetesClient kubernetes.Interface, instanceID string) *HpaOperations {
	return &HpaOperations{
		logger:           logger,
		kubernetesClient: kubernetesClient,
		instanceID:       instanceID,
	}
}

func ConvertTargetCPUToCustomMetric(targetCPUVal int32) asv2.MetricSpec {
	return asv2.MetricSpec{
		Type: asv2.ResourceMetricSourceType,
		Resource: &asv2.ResourceMetricSource{
			Name: corev1.ResourceCPU,
			Target: asv2.MetricTarget{
				Type:               asv2.UtilizationMetricType,
				AverageUtilization: &targetCPUVal,
			},
		},
	}
}

func getScaleTargetRef(deployment *appsv1.Deployment) asv2.CrossVersionObjectReference {
	return asv2.CrossVersionObjectReference{
		APIVersion: DeploymentVersion,
		Kind:       DeploymentKind,
		Name:       deployment.ObjectMeta.Name,
	}
}

func (hpaops *HpaOperations) CreateOrGetHpa(ctx context.Context, hpaName string, execStrategy *fv1.ExecutionStrategy,
	depl *appsv1.Deployment, deployLabels map[string]string, deployAnnotations map[string]string) (*asv2.HorizontalPodAutoscaler, error) {

	if depl == nil {
		return nil, errors.New("failed to create HPA, found empty deployment")
	}
	logger := otelUtils.LoggerWithTraceID(ctx, hpaops.logger)

	minRepl := int32(execStrategy.MinScale)
	if minRepl == 0 {
		minRepl = 1
	}
	maxRepl := int32(execStrategy.MaxScale)
	if maxRepl == 0 {
		maxRepl = minRepl
	}
	targetCPU := int32(execStrategy.TargetCPUPercent) // nolint: staticcheck
	var hpaMetrics []asv2.MetricSpec
	if targetCPU > 0 && targetCPU < 100 {
		hpaMetrics = append(hpaMetrics, ConvertTargetCPUToCustomMetric(targetCPU))
	}

	if execStrategy.Metrics != nil {
		hpaMetrics = append(hpaMetrics, execStrategy.Metrics...)
	}

	hpa := &asv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:        hpaName,
			Labels:      deployLabels,
			Annotations: deployAnnotations,
		},
		Spec: asv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: getScaleTargetRef(depl),
			MinReplicas:    &minRepl,
			MaxReplicas:    maxRepl,
			Metrics:        hpaMetrics,
			Behavior:       execStrategy.Behavior,
		},
	}

	existingHpa, err := hpaops.GetHpa(ctx, depl.ObjectMeta.Namespace, hpaName)
	if err == nil {
		// to adopt orphan service
		if existingHpa.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != hpaops.instanceID {
			existingHpa.Annotations = hpa.Annotations
			existingHpa.Labels = hpa.Labels
			existingHpa.Spec = hpa.Spec
			existingHpa, err = hpaops.kubernetesClient.AutoscalingV2().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Update(ctx, existingHpa, metav1.UpdateOptions{})
			if err != nil {
				logger.Warn("error adopting HPA", zap.Error(err),
					zap.String("HPA", hpaName), zap.String("ns", depl.ObjectMeta.Namespace))
				return nil, err
			}
		}
		return existingHpa, err
	} else if k8s_err.IsNotFound(err) {
		cHpa, err := hpaops.kubernetesClient.AutoscalingV2().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Create(ctx, hpa, metav1.CreateOptions{})
		if err != nil {
			if k8s_err.IsAlreadyExists(err) {
				cHpa, err = hpaops.kubernetesClient.AutoscalingV2().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Get(ctx, hpaName, metav1.GetOptions{})
			}
			if err != nil {
				return nil, err
			}
		}
		otelUtils.SpanTrackEvent(ctx, "hpaCreated", otelUtils.GetAttributesForHPA(cHpa)...)
		return cHpa, nil
	}
	return nil, err
}

func (hpaops *HpaOperations) GetHpa(ctx context.Context, ns, name string) (*asv2.HorizontalPodAutoscaler, error) {
	return hpaops.kubernetesClient.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
}

func (hpaops *HpaOperations) UpdateHpa(ctx context.Context, hpa *asv2.HorizontalPodAutoscaler) error {
	_, err := hpaops.kubernetesClient.AutoscalingV2().HorizontalPodAutoscalers(hpa.ObjectMeta.Namespace).Update(ctx, hpa, metav1.UpdateOptions{})
	return err
}

func (hpaops *HpaOperations) DeleteHpa(ctx context.Context, ns string, name string) error {
	return hpaops.kubernetesClient.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, name, metav1.DeleteOptions{})
}
