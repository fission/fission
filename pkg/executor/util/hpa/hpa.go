// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hpa

import (
	"context"
	"errors"

	appsv1 "k8s.io/api/apps/v1"
	asv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// Deployment Constants
const (
	DeploymentKind    = "Deployment"
	DeploymentVersion = "apps/v1"
)

type HpaOperations struct {
	logger                logr.Logger
	kubernetesClient      kubernetes.Interface
	instanceID            string
	enableOwnerReferences bool
}

func NewHpaOperations(logger logr.Logger, kubernetesClient kubernetes.Interface, instanceID string) *HpaOperations {
	return &HpaOperations{
		logger:                logger,
		kubernetesClient:      kubernetesClient,
		instanceID:            instanceID,
		enableOwnerReferences: utils.IsOwnerReferencesEnabled(),
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

// RewriteResourceMetricsToContainer converts pod-wide Resource cpu/memory
// metrics to ContainerResource metrics scoped to mainContainer. Pod-wide
// Resource metrics require *every* container in the pod to declare the
// resource request; sidecars (fetcher, user sidecars) and resourceless
// function containers make KCM fail the whole metric ("missing request for
// cpu in container ..."). Scoping to the function's main container sidesteps
// that and stops the idle fetcher from diluting the average. The CLI keeps
// emitting pod-wide Resource metrics (it cannot know the runtime container
// name); the executor normalizes them here. Non-Resource metrics pass
// through untouched.
func RewriteResourceMetricsToContainer(metrics []asv2.MetricSpec, mainContainer string, logger logr.Logger) []asv2.MetricSpec {
	if len(metrics) == 0 {
		return metrics
	}
	if mainContainer == "" {
		logger.Info("WARNING: cannot scope pod-wide HPA resource metrics to the function container; main container name is empty, leaving metrics pod-wide")
		return metrics
	}
	out := make([]asv2.MetricSpec, len(metrics))
	for i, m := range metrics {
		if m.Type == asv2.ResourceMetricSourceType && m.Resource != nil &&
			(m.Resource.Name == corev1.ResourceCPU || m.Resource.Name == corev1.ResourceMemory) {
			logger.V(1).Info("rewrote pod-wide resource metric to container metric",
				"resource", m.Resource.Name, "container", mainContainer)
			out[i] = asv2.MetricSpec{
				Type: asv2.ContainerResourceMetricSourceType,
				ContainerResource: &asv2.ContainerResourceMetricSource{
					Name:      m.Resource.Name,
					Container: mainContainer,
					Target:    m.Resource.Target,
				},
			}
			continue
		}
		out[i] = m
	}
	return out
}

func getScaleTargetRef(deployment *appsv1.Deployment) asv2.CrossVersionObjectReference {
	return asv2.CrossVersionObjectReference{
		APIVersion: DeploymentVersion,
		Kind:       DeploymentKind,
		Name:       deployment.Name,
	}
}

func (hpaops *HpaOperations) CreateOrGetHpa(ctx context.Context, fn *fv1.Function, hpaName string, execStrategy *fv1.ExecutionStrategy,
	mainContainer string, depl *appsv1.Deployment, deployLabels map[string]string, deployAnnotations map[string]string) (*asv2.HorizontalPodAutoscaler, error) {

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

	hpaMetrics = RewriteResourceMetricsToContainer(hpaMetrics, mainContainer, logger)

	var ownerReferences []metav1.OwnerReference
	if hpaops.enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(fn, schema.GroupVersionKind{
				Group:   "fission.io",
				Version: "v1",
				Kind:    "Function",
			}),
		}
	}

	hpa := &asv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:            hpaName,
			Labels:          deployLabels,
			Annotations:     deployAnnotations,
			OwnerReferences: ownerReferences,
		},
		Spec: asv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: getScaleTargetRef(depl),
			MinReplicas:    &minRepl,
			MaxReplicas:    maxRepl,
			Metrics:        hpaMetrics,
			Behavior:       execStrategy.Behavior,
		},
	}

	existingHpa, err := hpaops.GetHpa(ctx, depl.Namespace, hpaName)
	if err == nil {
		needsUpdate := false
		// to adopt orphan service
		if existingHpa.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != hpaops.instanceID {
			existingHpa.Annotations = hpa.Annotations
			existingHpa.Labels = hpa.Labels
			existingHpa.OwnerReferences = hpa.OwnerReferences
			existingHpa.Spec = hpa.Spec
			needsUpdate = true
		}
		// Reconcile metric drift so HPAs created before this normalization
		// (pod-wide Resource metrics) get rewritten to ContainerResource
		// metrics in place, and so CLI-driven updates that re-emit pod-wide
		// metrics are re-normalized.
		if !apiequality.Semantic.DeepEqual(existingHpa.Spec.Metrics, hpa.Spec.Metrics) {
			existingHpa.Spec.Metrics = hpa.Spec.Metrics
			needsUpdate = true
		}
		if needsUpdate {
			existingHpa, err = hpaops.kubernetesClient.AutoscalingV2().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Update(ctx, existingHpa, metav1.UpdateOptions{})
			if err != nil {
				logger.Error(err, "error reconciling HPA", "HPA", hpaName, "ns", depl.Namespace)
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
