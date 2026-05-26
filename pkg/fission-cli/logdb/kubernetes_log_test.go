// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logdb

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

const fnNamespace = "fns"

func testFunction() *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: fnNamespace, UID: "uid-1"},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "node-env", Namespace: fnNamespace},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypePoolmgr},
			},
		},
	}
}

func functionPod(name, resourceVersion string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       fnNamespace,
			ResourceVersion: resourceVersion,
			Labels: map[string]string{
				fv1.FUNCTION_UID:          "uid-1",
				fv1.ENVIRONMENT_NAME:      "node-env",
				fv1.ENVIRONMENT_NAMESPACE: fnNamespace,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node1",
			Containers: []corev1.Container{
				{Name: "fetcher"}, // skipped by streamContainerLog
				{Name: "hello"},
			},
		},
	}
}

func TestGetFunctionPodLogs(t *testing.T) {
	t.Run("streams the newest pod's container logs", func(t *testing.T) {
		client := cmd.Client{KubernetesClient: fake.NewClientset(
			functionPod("pod-old", "1"),
			functionPod("pod-new", "9"),
		)}
		var buf bytes.Buffer
		filter := LogFilter{FunctionObject: testFunction(), PodNamespace: fnNamespace, RecordLimit: 100}

		require.NoError(t, GetFunctionPodLogs(t.Context(), client, filter, &buf))
		assert.NotEmpty(t, buf.String(), "fake clientset returns canned log content")
	})

	t.Run("details prepends function metadata", func(t *testing.T) {
		client := cmd.Client{KubernetesClient: fake.NewClientset(functionPod("pod1", "1"))}
		var buf bytes.Buffer
		filter := LogFilter{FunctionObject: testFunction(), PodNamespace: fnNamespace, RecordLimit: 100, Details: true}

		require.NoError(t, GetFunctionPodLogs(t.Context(), client, filter, &buf))
		assert.Contains(t, buf.String(), "Function=hello")
	})

	t.Run("all pods", func(t *testing.T) {
		client := cmd.Client{KubernetesClient: fake.NewClientset(
			functionPod("pod1", "1"),
			functionPod("pod2", "2"),
		)}
		var buf bytes.Buffer
		filter := LogFilter{FunctionObject: testFunction(), PodNamespace: fnNamespace, RecordLimit: 100, AllPods: true}

		require.NoError(t, GetFunctionPodLogs(t.Context(), client, filter, &buf))
		assert.NotEmpty(t, buf.String())
	})

	t.Run("no pods returns an error", func(t *testing.T) {
		client := cmd.Client{KubernetesClient: fake.NewClientset()}
		var buf bytes.Buffer
		filter := LogFilter{FunctionObject: testFunction(), PodNamespace: fnNamespace, RecordLimit: 100}

		err := GetFunctionPodLogs(t.Context(), client, filter, &buf)
		require.Error(t, err)
	})

	t.Run("container executor uses a function-uid-only selector", func(t *testing.T) {
		fn := testFunction()
		fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeContainer
		pod := functionPod("cpod", "1")
		// Only the function-uid label is required for container functions.
		pod.Labels = map[string]string{fv1.FUNCTION_UID: "uid-1"}
		client := cmd.Client{KubernetesClient: fake.NewClientset(pod)}
		var buf bytes.Buffer
		filter := LogFilter{FunctionObject: fn, PodNamespace: fnNamespace, RecordLimit: 100}

		require.NoError(t, GetFunctionPodLogs(t.Context(), client, filter, &buf))
		assert.NotEmpty(t, buf.String())
	})
}
