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
	"strings"
	"testing"

	"github.com/dchest/uniuri"
	appsv1 "k8s.io/api/apps/v1"
	asv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	kubernetesClient := fake.NewSimpleClientset()
	instanceID := strings.ToLower(uniuri.NewLen(8))
	ns := "test-namespace"
	hpaops := NewHpaOperations(logger, kubernetesClient, instanceID)
	if hpaops.instanceID != instanceID {
		t.Errorf("Expected instanceID to be %v, got %v", instanceID, hpaops.instanceID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deployLabels := map[string]string{
		"test-label": "test-label-value",
	}
	deployAnnotations := map[string]string{
		"test-annotation": "test-annotation-value",
	}
	// Test CreateHPA
	hpa, err := hpaops.CreateOrGetHpa(ctx, "test-hpa",
		&fv1.ExecutionStrategy{
			ExecutorType:          fv1.ExecutorTypeNewdeploy,
			MinScale:              1,
			MaxScale:              5,
			TargetCPUPercent:      50,
			SpecializationTimeout: 300,
		},
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
	if hpa.Spec.Metrics[0].Type != asv2.ResourceMetricSourceType {
		t.Errorf("Expected metric type to be Resource, got %v", hpa.Spec.Metrics[0].Type)
	}
	if hpa.Spec.Metrics[0].Resource.Name != corev1.ResourceCPU {
		t.Errorf("Expected metric name to be cpu, got %v", hpa.Spec.Metrics[0].Resource.Name)
	}
	if hpa.Spec.Metrics[0].Resource.Target.Type != asv2.UtilizationMetricType {
		t.Errorf("Expected metric target type to be Utilization, got %v", hpa.Spec.Metrics[0].Resource.Target.Type)
	}
	if hpa.Spec.Metrics[0].Resource.Target.AverageUtilization == nil {
		t.Errorf("Expected metric target average utilization to be set, got nil")
	}
	if *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 50 {
		t.Errorf("Expected metric target average utilization to be 50, got %v", *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization)
	}
	if hpa.ObjectMeta.Labels["test-label"] != "test-label-value" {
		t.Errorf("Expected label to be set, got %v", hpa.ObjectMeta.Labels["test-label"])
	}
	if hpa.ObjectMeta.Annotations["test-annotation"] != "test-annotation-value" {
		t.Errorf("Expected annotation to be set, got %v", hpa.ObjectMeta.Annotations["test-annotation"])
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
