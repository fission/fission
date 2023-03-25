package v1

import (
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func newFunction() *Function {
	return &Function{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{},
		Spec: FunctionSpec{
			Environment:     EnvironmentReference{},
			Package:         FunctionPackageRef{},
			Secrets:         []SecretReference{},
			ConfigMaps:      []ConfigMapReference{},
			Resources:       apiv1.ResourceRequirements{},
			InvokeStrategy:  InvokeStrategy{},
			FunctionTimeout: 0,
			IdleTimeout:     new(int),
			Concurrency:     0,
			RequestsPerPod:  0,
			OnceOnly:        false,
			PodSpec:         &apiv1.PodSpec{},
		},
	}
}

func TestFunctionGetConcurrencyWhenConcurrencyIsZero(t *testing.T) {
	function := newFunction()
	result := function.GetConcurrency()

	if result != 500 {
		t.Errorf("expected concurrency to be 500, but got %d", result)
	}
}

func TestFunctionGetConcurrencyWhenConcurrencyIsOtherThenZero(t *testing.T) {
	function := newFunction()
	function.Spec.Concurrency = 5
	result := function.GetConcurrency()

	if result != 5 {
		t.Errorf("expected concurrency to be 5, but got %d", result)
	}
}

func TestFunctionGetRequestsPerPodWhenRPPIsZero(t *testing.T) {
	function := newFunction()
	result := function.GetRequestPerPod()

	if result != 1 {
		t.Errorf("expected requests per pod to be 1, but got %d", result)
	}
}

func TestFunctionGetRequestsPerPodWhenRPPIsGreaterThanZero(t *testing.T) {
	function := newFunction()
	function.Spec.RequestsPerPod = 10
	result := function.GetRequestPerPod()

	if result != 10 {
		t.Errorf("expected requests per pod to be 10, but got %d", result)
	}
}
