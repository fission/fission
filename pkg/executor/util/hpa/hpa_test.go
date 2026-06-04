// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hpa

import (
	"strings"
	"testing"

	"github.com/dchest/uniuri"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	asv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestConvertTargetCPUToCustomMetric(t *testing.T) {
	metricSpec := ConvertTargetCPUToCustomMetric(50)
	if metricSpec.Type != asv2.ResourceMetricSourceType {
		t.Errorf("Expected metric type to be Resource, got %v", metricSpec.Type)
	}
	if metricSpec.Resource.Name != corev1.ResourceCPU {
		t.Errorf("Expected metric name to be cpu, got %v", metricSpec.Resource.Name)
	}
	if metricSpec.Resource.Target.Type != asv2.UtilizationMetricType {
		t.Errorf("Expected metric target type to be Utilization, got %v", metricSpec.Resource.Target.Type)
	}
	if metricSpec.Resource.Target.AverageUtilization == nil {
		t.Errorf("Expected metric target average utilization to be set, got nil")
	}
}

func TestHpaOps(t *testing.T) {
	logger := loggerfactory.GetLogger()
	kubernetesClient := fake.NewClientset()
	instanceID := strings.ToLower(uniuri.NewLen(8))
	ns := "test-namespace"
	hpaops := NewHpaOperations(logger, kubernetesClient, instanceID)
	if hpaops.instanceID != instanceID {
		t.Errorf("Expected instanceID to be %v, got %v", instanceID, hpaops.instanceID)
	}

	ctx := t.Context()

	deployLabels := map[string]string{
		"test-label": "test-label-value",
	}
	deployAnnotations := map[string]string{
		"test-annotation": "test-annotation-value",
	}
	// Test CreateHPA
	hpa, err := hpaops.CreateOrGetHpa(ctx,
		&fv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-fn",
				UID:  uuid.NewUUID(),
			},
		},
		"test-hpa",
		&fv1.ExecutionStrategy{
			ExecutorType:          fv1.ExecutorTypeNewdeploy,
			MinScale:              1,
			MaxScale:              5,
			TargetCPUPercent:      50,
			SpecializationTimeout: 300,
		},
		"test-fn",
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: ns,
			},
		},
		deployLabels,
		deployAnnotations)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if *hpa.Spec.MinReplicas != 1 {
		t.Errorf("Expected min replicas to be 1, got %v", hpa.Spec.MinReplicas)
	}
	if hpa.Spec.MaxReplicas != 5 {
		t.Errorf("Expected max replicas to be 5, got %v", hpa.Spec.MaxReplicas)
	}
	// The pod-wide CPU Resource metric derived from TargetCPUPercent is
	// normalized to a ContainerResource metric scoped to the main container.
	if hpa.Spec.Metrics[0].Type != asv2.ContainerResourceMetricSourceType {
		t.Errorf("Expected metric type to be ContainerResource, got %v", hpa.Spec.Metrics[0].Type)
	}
	if hpa.Spec.Metrics[0].ContainerResource.Name != corev1.ResourceCPU {
		t.Errorf("Expected metric name to be cpu, got %v", hpa.Spec.Metrics[0].ContainerResource.Name)
	}
	if hpa.Spec.Metrics[0].ContainerResource.Container != "test-fn" {
		t.Errorf("Expected metric container to be test-fn, got %v", hpa.Spec.Metrics[0].ContainerResource.Container)
	}
	if hpa.Spec.Metrics[0].ContainerResource.Target.Type != asv2.UtilizationMetricType {
		t.Errorf("Expected metric target type to be Utilization, got %v", hpa.Spec.Metrics[0].ContainerResource.Target.Type)
	}
	if hpa.Spec.Metrics[0].ContainerResource.Target.AverageUtilization == nil {
		t.Errorf("Expected metric target average utilization to be set, got nil")
	}
	if *hpa.Spec.Metrics[0].ContainerResource.Target.AverageUtilization != 50 {
		t.Errorf("Expected metric target average utilization to be 50, got %v", *hpa.Spec.Metrics[0].ContainerResource.Target.AverageUtilization)
	}
	if hpa.Labels["test-label"] != "test-label-value" {
		t.Errorf("Expected label to be set, got %v", hpa.Labels["test-label"])
	}
	if hpa.Annotations["test-annotation"] != "test-annotation-value" {
		t.Errorf("Expected annotation to be set, got %v", hpa.Annotations["test-annotation"])
	}

	hpa, err = hpaops.GetHpa(ctx, ns, "test-hpa")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	hpa.Spec.MaxReplicas = 10

	// Test UpdateHPA
	err = hpaops.UpdateHpa(ctx, hpa)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	hpa, err = hpaops.GetHpa(ctx, ns, "test-hpa")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if hpa.Spec.MaxReplicas != 10 {
		t.Errorf("Expected max replicas to be 10, got %v", hpa.Spec.MaxReplicas)
	}

	// Test DeleteHPA
	err = hpaops.DeleteHpa(ctx, ns, "test-hpa")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	_, err = hpaops.GetHpa(ctx, ns, "test-hpa")
	if err == nil {
		t.Errorf("Expected error, got nil")
	}
}

func resourceMetric(name corev1.ResourceName, target asv2.MetricTarget) asv2.MetricSpec {
	return asv2.MetricSpec{
		Type:     asv2.ResourceMetricSourceType,
		Resource: &asv2.ResourceMetricSource{Name: name, Target: target},
	}
}

func TestRewriteResourceMetricsToContainer(t *testing.T) {
	t.Parallel()

	logger := loggerfactory.GetLogger()
	util80 := int32(80)
	mem := resource.MustParse("256Mi")

	cpuUtil := resourceMetric(corev1.ResourceCPU, asv2.MetricTarget{
		Type: asv2.UtilizationMetricType, AverageUtilization: &util80,
	})
	memAvg := resourceMetric(corev1.ResourceMemory, asv2.MetricTarget{
		Type: asv2.AverageValueMetricType, AverageValue: &mem,
	})
	podsMetric := asv2.MetricSpec{Type: asv2.PodsMetricSourceType, Pods: &asv2.PodsMetricSource{}}
	externalMetric := asv2.MetricSpec{Type: asv2.ExternalMetricSourceType, External: &asv2.ExternalMetricSource{}}
	objectMetric := asv2.MetricSpec{Type: asv2.ObjectMetricSourceType, Object: &asv2.ObjectMetricSource{}}
	alreadyContainer := asv2.MetricSpec{
		Type: asv2.ContainerResourceMetricSourceType,
		ContainerResource: &asv2.ContainerResourceMetricSource{
			Name: corev1.ResourceCPU, Container: "main",
			Target: asv2.MetricTarget{Type: asv2.UtilizationMetricType, AverageUtilization: &util80},
		},
	}
	// Resource type with nil Resource field — defensive, must pass through.
	nilResource := asv2.MetricSpec{Type: asv2.ResourceMetricSourceType, Resource: nil}

	tests := []struct {
		name          string
		input         []asv2.MetricSpec
		mainContainer string
		want          []asv2.MetricSpec
	}{
		{
			name:          "cpu utilization resource rewritten",
			input:         []asv2.MetricSpec{cpuUtil},
			mainContainer: "fn",
			want: []asv2.MetricSpec{{
				Type: asv2.ContainerResourceMetricSourceType,
				ContainerResource: &asv2.ContainerResourceMetricSource{
					Name: corev1.ResourceCPU, Container: "fn", Target: cpuUtil.Resource.Target,
				},
			}},
		},
		{
			name:          "memory averagevalue resource rewritten",
			input:         []asv2.MetricSpec{memAvg},
			mainContainer: "fn",
			want: []asv2.MetricSpec{{
				Type: asv2.ContainerResourceMetricSourceType,
				ContainerResource: &asv2.ContainerResourceMetricSource{
					Name: corev1.ResourceMemory, Container: "fn", Target: memAvg.Resource.Target,
				},
			}},
		},
		{
			// One list mixing both rewritable resources around a Pods metric:
			// both Resource entries must be rewritten, the Pods entry left
			// untouched, and every entry must keep its original index.
			name:          "mixed cpu and memory resources around a pods metric",
			input:         []asv2.MetricSpec{cpuUtil, podsMetric, memAvg},
			mainContainer: "fn",
			want: []asv2.MetricSpec{
				{
					Type: asv2.ContainerResourceMetricSourceType,
					ContainerResource: &asv2.ContainerResourceMetricSource{
						Name: corev1.ResourceCPU, Container: "fn", Target: cpuUtil.Resource.Target,
					},
				},
				podsMetric,
				{
					Type: asv2.ContainerResourceMetricSourceType,
					ContainerResource: &asv2.ContainerResourceMetricSource{
						Name: corev1.ResourceMemory, Container: "fn", Target: memAvg.Resource.Target,
					},
				},
			},
		},
		{
			name:          "non-resource metrics unchanged",
			input:         []asv2.MetricSpec{podsMetric, externalMetric, objectMetric, alreadyContainer},
			mainContainer: "fn",
			want:          []asv2.MetricSpec{podsMetric, externalMetric, objectMetric, alreadyContainer},
		},
		{
			name:          "empty main container returns input unchanged",
			input:         []asv2.MetricSpec{cpuUtil},
			mainContainer: "",
			want:          []asv2.MetricSpec{cpuUtil},
		},
		{
			name:          "nil resource field with resource type unchanged",
			input:         []asv2.MetricSpec{nilResource},
			mainContainer: "fn",
			want:          []asv2.MetricSpec{nilResource},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RewriteResourceMetricsToContainer(tc.input, tc.mainContainer, logger)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCreateOrGetHpaMetricsReconcile(t *testing.T) {
	logger := loggerfactory.GetLogger()
	instanceID := strings.ToLower(uniuri.NewLen(8))
	ns := "test-namespace"
	util50 := int32(50)

	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "test-fn", UID: uuid.NewUUID()}}
	execStrategy := &fv1.ExecutionStrategy{
		ExecutorType: fv1.ExecutorTypeNewdeploy,
		MinScale:     1,
		MaxScale:     5,
		Metrics: []asv2.MetricSpec{resourceMetric(corev1.ResourceCPU, asv2.MetricTarget{
			Type: asv2.UtilizationMetricType, AverageUtilization: &util50,
		})},
	}
	depl := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "test-deployment", Namespace: ns}}

	t.Run("create new uses container resource metric", func(t *testing.T) {
		client := fake.NewClientset()
		hpaops := NewHpaOperations(logger, client, instanceID)
		hpa, err := hpaops.CreateOrGetHpa(t.Context(), fn, "test-hpa", execStrategy, "fn-container", depl, nil, nil)
		require.NoError(t, err)
		require.Len(t, hpa.Spec.Metrics, 1)
		assert.Equal(t, asv2.ContainerResourceMetricSourceType, hpa.Spec.Metrics[0].Type)
		require.NotNil(t, hpa.Spec.Metrics[0].ContainerResource)
		assert.Equal(t, "fn-container", hpa.Spec.Metrics[0].ContainerResource.Container)
		assert.Equal(t, corev1.ResourceCPU, hpa.Spec.Metrics[0].ContainerResource.Name)
	})

	t.Run("existing pod-wide metric reconciled to container resource", func(t *testing.T) {
		seeded := &asv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-hpa",
				Namespace:   ns,
				Annotations: map[string]string{fv1.EXECUTOR_INSTANCEID_LABEL: instanceID},
			},
			Spec: asv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: getScaleTargetRef(depl),
				MinReplicas:    &util50,
				MaxReplicas:    5,
				Metrics: []asv2.MetricSpec{resourceMetric(corev1.ResourceCPU, asv2.MetricTarget{
					Type: asv2.UtilizationMetricType, AverageUtilization: &util50,
				})},
			},
		}
		client := fake.NewClientset(seeded)
		hpaops := NewHpaOperations(logger, client, instanceID)
		hpa, err := hpaops.CreateOrGetHpa(t.Context(), fn, "test-hpa", execStrategy, "fn-container", depl, nil, nil)
		require.NoError(t, err)
		require.Len(t, hpa.Spec.Metrics, 1)
		assert.Equal(t, asv2.ContainerResourceMetricSourceType, hpa.Spec.Metrics[0].Type)
		require.NotNil(t, hpa.Spec.Metrics[0].ContainerResource)
		assert.Equal(t, "fn-container", hpa.Spec.Metrics[0].ContainerResource.Container)

		var updates int
		for _, a := range client.Actions() {
			if a.GetVerb() == "update" && a.GetResource().Resource == "horizontalpodautoscalers" {
				updates++
			}
		}
		assert.Equal(t, 1, updates, "expected exactly one update to rewrite the drifted metric")
	})

	t.Run("already-correct metric issues no update", func(t *testing.T) {
		seeded := &asv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-hpa",
				Namespace:   ns,
				Annotations: map[string]string{fv1.EXECUTOR_INSTANCEID_LABEL: instanceID},
			},
			Spec: asv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: getScaleTargetRef(depl),
				MinReplicas:    &util50,
				MaxReplicas:    5,
				Metrics: []asv2.MetricSpec{{
					Type: asv2.ContainerResourceMetricSourceType,
					ContainerResource: &asv2.ContainerResourceMetricSource{
						Name: corev1.ResourceCPU, Container: "fn-container",
						Target: asv2.MetricTarget{Type: asv2.UtilizationMetricType, AverageUtilization: &util50},
					},
				}},
			},
		}
		client := fake.NewClientset(seeded)
		hpaops := NewHpaOperations(logger, client, instanceID)
		_, err := hpaops.CreateOrGetHpa(t.Context(), fn, "test-hpa", execStrategy, "fn-container", depl, nil, nil)
		require.NoError(t, err)

		for _, a := range client.Actions() {
			if a.GetVerb() == "update" && a.GetResource().Resource == "horizontalpodautoscalers" {
				t.Errorf("expected no update action for an already-correct HPA, got one")
			}
		}
	})
}
