// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func describeFunction() *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hello", Namespace: "default",
			Labels: map[string]string{"team": "core"},
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "nodejs", Namespace: "default"},
			Package:     fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Name: "hello-pkg", Namespace: "default"}},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypePoolmgr},
			},
		},
		Status: fv1.FunctionStatus{Conditions: []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "FunctionReady", Message: "function is ready"},
		}},
	}
}

func describeFunctionPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "poolmgr-nodejs-hello-abc", Namespace: "fission-function",
			Labels: map[string]string{
				fv1.FUNCTION_NAME:      "hello",
				fv1.FUNCTION_NAMESPACE: "default",
				fv1.EXECUTOR_TYPE:      "poolmgr",
				fv1.MANAGED:            "false",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, PodIP: "10.0.0.5",
			ContainerStatuses: []corev1.ContainerStatus{{Ready: true}, {Ready: true}},
		},
	}
}

func setDescribeClients(t *testing.T, fissionObjs []runtime.Object, pods ...runtime.Object) {
	t.Helper()
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(fissionObjs...),
		KubernetesClient: k8sfake.NewClientset(pods...),
		Namespace:        "default",
	})
}

func describeInput(name, ns string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, name)
	in.Set(flagkey.NamespaceFunction, ns)
	return in
}

func TestFunctionDescribe(t *testing.T) {
	t.Run("renders summary, conditions, package build status, and pods", func(t *testing.T) {
		fn := describeFunction()
		pkg := &fv1.Package{
			ObjectMeta: metav1.ObjectMeta{Name: "hello-pkg", Namespace: "default"},
			Status:     fv1.PackageStatus{BuildStatus: fv1.BuildStatusSucceeded},
		}
		// A second pod with 1 of 2 containers ready, to lock the READY column as
		// ready/total (a total/ready regression would render "2/1").
		partial := describeFunctionPod()
		partial.Name = "poolmgr-nodejs-hello-def"
		partial.Status.ContainerStatuses = []corev1.ContainerStatus{{Ready: true}, {Ready: false}}
		setDescribeClients(t, []runtime.Object{fn, pkg}, describeFunctionPod(), partial)

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "hello", "function name")
		assert.Contains(t, out, "nodejs", "environment")
		assert.Contains(t, out, "poolmgr", "executor type")
		assert.Contains(t, out, "Ready", "function condition")
		assert.Contains(t, out, "succeeded", "package build status")
		assert.Contains(t, out, "poolmgr-nodejs-hello-abc", "pod name")
		assert.Contains(t, out, "2/2", "fully-ready pod count")
		assert.Contains(t, out, "1/2", "partially-ready pod count (ready/total order)")
		assert.Contains(t, out, "Invocable", "invocability headline")
		assert.Contains(t, out, "Yes", "ready function with a warm pod is invocable")
	})

	t.Run("a not-Ready function reports not invocable", func(t *testing.T) {
		fn := describeFunction()
		fn.Status.Conditions = []metav1.Condition{
			{Type: fv1.FunctionConditionReady, Status: metav1.ConditionFalse, Reason: "PackageFailed", Message: "build failed"},
		}
		pkg := &fv1.Package{
			ObjectMeta: metav1.ObjectMeta{Name: "hello-pkg", Namespace: "default"},
			Status:     fv1.PackageStatus{BuildStatus: fv1.BuildStatusFailed},
		}
		setDescribeClients(t, []runtime.Object{fn, pkg})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "Invocable", "invocability headline")
		assert.Contains(t, out, "No", "not-Ready function is not invocable")
	})

	t.Run("a failed build shows the build log", func(t *testing.T) {
		fn := describeFunction()
		pkg := &fv1.Package{
			ObjectMeta: metav1.ObjectMeta{Name: "hello-pkg", Namespace: "default"},
			Status: fv1.PackageStatus{
				BuildStatus: fv1.BuildStatusFailed,
				BuildLog:    `npm ERR! missing script: build`,
			},
		}
		setDescribeClients(t, []runtime.Object{fn, pkg})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "failed", "build status")
		assert.Contains(t, out, "npm ERR! missing script: build", "build log on failure")
	})

	t.Run("a missing package degrades gracefully, not an error", func(t *testing.T) {
		fn := describeFunction() // references hello-pkg, which is absent from the clientset
		setDescribeClients(t, []runtime.Object{fn})

		out := captureStdout(t, func() error { return Describe(describeInput("hello", "default")) })

		assert.Contains(t, out, "hello", "still renders the function summary")
		assert.NotContains(t, out, "succeeded")
	})

	t.Run("a missing function is an error", func(t *testing.T) {
		setDescribeClients(t, []runtime.Object{describeFunction()})
		err := Describe(describeInput("does-not-exist", "default"))
		require.Error(t, err)
	})
}
