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

// versionedFunctionPod is functionPod plus an RFC-0025
// fission.io/function-version label, for exercising LogFilter.Version.
func versionedFunctionPod(name, resourceVersion, version string) *corev1.Pod {
	pod := functionPod(name, resourceVersion)
	pod.Labels[fv1.FUNCTION_VERSION] = version
	return pod
}

// TestSelectFunctionPodsVersionFilter guards the RFC-0025 --alias/--version
// selector addition and why it is genuinely load-bearing, not cosmetic:
// selectFunctionPods's non-AllPods path picks only the single newest pod
// (highest resourceVersion) among the *selected* set. Without narrowing the
// selector to LogFilter.Version first, that "newest" pod could belong to an
// entirely different, unrelated version -- silently reading the wrong pod's
// logs.
func TestSelectFunctionPodsVersionFilter(t *testing.T) {
	older := versionedFunctionPod("pod-v1", "1", "hello-v1")
	newer := versionedFunctionPod("pod-v2", "9", "hello-v2")

	t.Run("without a version filter, the globally newest pod wins", func(t *testing.T) {
		client := cmd.Client{KubernetesClient: fake.NewClientset(older, newer)}
		filter := LogFilter{FunctionObject: testFunction(), PodNamespace: fnNamespace, RecordLimit: 100}

		pods, err := selectFunctionPods(t.Context(), client, filter)
		require.NoError(t, err)
		require.Len(t, pods, 1)
		assert.Equal(t, "pod-v2", pods[0].Name)
	})

	t.Run("a version filter scopes selection to that version's pods, even the older one", func(t *testing.T) {
		client := cmd.Client{KubernetesClient: fake.NewClientset(older, newer)}
		filter := LogFilter{FunctionObject: testFunction(), PodNamespace: fnNamespace, RecordLimit: 100, Version: "hello-v1"}

		pods, err := selectFunctionPods(t.Context(), client, filter)
		require.NoError(t, err)
		require.Len(t, pods, 1)
		assert.Equal(t, "pod-v1", pods[0].Name)
	})

	t.Run("a version filter matching no pods errors", func(t *testing.T) {
		client := cmd.Client{KubernetesClient: fake.NewClientset(older)}
		filter := LogFilter{FunctionObject: testFunction(), PodNamespace: fnNamespace, RecordLimit: 100, Version: "hello-v9"}

		_, err := selectFunctionPods(t.Context(), client, filter)
		require.Error(t, err)
	})
}
