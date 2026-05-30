// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateKubeName(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKubeName("f", "valid-name"))
	require.Error(t, ValidateKubeName("f", "Invalid_Name"))
	require.Error(t, ValidateKubeName("f", "-bad"))
}

func TestValidateKubePort(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKubePort("p", 8080))
	require.Error(t, ValidateKubePort("p", -1))
	require.Error(t, ValidateKubePort("p", 70000))
}

func TestValidateKubeLabel(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKubeLabel("l", map[string]string{}), "empty labels are valid")
	require.Error(t, ValidateKubeLabel("l", map[string]string{"in valid key": "v"}))
}

func TestValidateKubeReference(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateKubeReference("ref", "name", "ns"))
	require.NoError(t, ValidateKubeReference("ref", "name", ""), "empty namespace is allowed")
	require.Error(t, ValidateKubeReference("ref", "Bad_Name", "ns"))
	require.Error(t, ValidateKubeReference("ref", "name", "Bad_NS"))
}

func TestIsValidCronSpec(t *testing.T) {
	t.Parallel()
	require.NoError(t, IsValidCronSpec("0 * * * *"))
	require.NoError(t, IsValidCronSpec("@every 1h"))
	require.Error(t, IsValidCronSpec("not a cron spec"))
}

func TestChecksumValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, Checksum{Type: ChecksumTypeSHA256}.Validate())
	require.Error(t, Checksum{Type: "md5"}.Validate())
}

func TestArchiveValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, Archive{Type: ArchiveTypeLiteral}.Validate())
	require.NoError(t, Archive{Type: ArchiveTypeUrl}.Validate())
	require.Error(t, Archive{Type: "tarball"}.Validate())
	require.Error(t, Archive{Checksum: Checksum{Type: "md5"}}.Validate())
}

func TestReferenceValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, EnvironmentReference{Name: "env", Namespace: "ns"}.Validate())
	require.NoError(t, SecretReference{Name: "sec"}.Validate())
	require.NoError(t, ConfigMapReference{Name: "cm"}.Validate())
	require.NoError(t, PackageRef{Name: "pkg"}.Validate())
	require.Error(t, EnvironmentReference{Name: "Bad_Name"}.Validate())
}

func TestPackageStatusValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, PackageStatus{BuildStatus: BuildStatusSucceeded}.Validate())
	// Empty BuildStatus is the not-yet-processed state admitted on create once
	// the /status subresource strips the defaulting webhook's status.
	require.NoError(t, PackageStatus{}.Validate())
	require.Error(t, PackageStatus{BuildStatus: "weird"}.Validate())
}

func TestExecutionStrategyValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr}.Validate())

	require.Error(t, ExecutionStrategy{ExecutorType: "bad"}.Validate())

	t.Run("newdeploy scale bounds", func(t *testing.T) {
		require.NoError(t, ExecutionStrategy{ExecutorType: ExecutorTypeNewdeploy, MinScale: 1, MaxScale: 3, TargetCPUPercent: 50}.Validate())
		require.Error(t, ExecutionStrategy{ExecutorType: ExecutorTypeNewdeploy, MaxScale: 0}.Validate(), "max scale must be > 0")
		require.Error(t, ExecutionStrategy{ExecutorType: ExecutorTypeNewdeploy, MinScale: 5, MaxScale: 2}.Validate(), "max < min")
		require.Error(t, ExecutionStrategy{ExecutorType: ExecutorTypeNewdeploy, MaxScale: 3, TargetCPUPercent: 200}.Validate(), "cpu out of range")
	})
}

func TestInvokeStrategyValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, InvokeStrategy{
		StrategyType:      StrategyTypeExecution,
		ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr},
	}.Validate())
	require.Error(t, InvokeStrategy{StrategyType: "weird", ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr}}.Validate())
}

func TestFunctionReferenceValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"}.Validate())
	require.NoError(t, FunctionReference{Type: FunctionReferenceTypeFunctionWeights}.Validate())
	require.Error(t, FunctionReference{Type: "bogus"}.Validate())
	require.Error(t, FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "Bad_Name"}.Validate())
}

func TestFunctionSpecValidate(t *testing.T) {
	t.Parallel()
	require.Error(t,
		FunctionSpec{InvokeStrategy: InvokeStrategy{
			StrategyType:      StrategyTypeExecution,
			ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypeContainer, MaxScale: 1},
		}}.Validate(),
		"container executor without a pod spec should fail")
}

func TestRuntimeValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, Runtime{LoadEndpointPort: 8000, FunctionEndpointPort: 8001}.Validate())
	require.Error(t, Runtime{LoadEndpointPort: 70000}.Validate())
}

func TestEnvironmentSpecValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, EnvironmentSpec{Version: 2, AllowedFunctionsPerContainer: AllowedFunctionsPerContainerSingle}.Validate())
	require.Error(t, EnvironmentSpec{Version: 0}.Validate(), "version out of range")
	require.Error(t, EnvironmentSpec{Version: 2, Poolsize: -1}.Validate())
	require.Error(t, EnvironmentSpec{Version: 2, AllowedFunctionsPerContainer: "many"}.Validate())
}

func TestHTTPTriggerSpecValidate(t *testing.T) {
	t.Parallel()
	valid := HTTPTriggerSpec{
		Methods:           []string{"GET", "POST"},
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}
	require.NoError(t, valid.Validate())

	bad := HTTPTriggerSpec{
		Method:            "FETCH",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}
	require.Error(t, bad.Validate())
}

func TestIngressConfigValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, IngressConfig{Path: "/foo", Host: "*"}.Validate())
	require.Error(t, IngressConfig{Path: "no-leading-slash"}.Validate())
}

func TestKubernetesWatchTriggerSpecValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, KubernetesWatchTriggerSpec{
		Type:              "POD",
		Namespace:         "default",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}.Validate())
	require.Error(t, KubernetesWatchTriggerSpec{
		Type:              "NOTAKIND",
		Namespace:         "default",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}.Validate())
}

func TestTimeTriggerSpecValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, TimeTriggerSpec{
		Cron:              "0 * * * *",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}.Validate())
	require.Error(t, TimeTriggerSpec{
		Cron:              "every-other-tuesday",
		FunctionReference: FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"},
	}.Validate())
}

func TestCRDValidate(t *testing.T) {
	t.Parallel()
	meta := metav1.ObjectMeta{Name: "ok", Namespace: "default"}

	t.Run("function", func(t *testing.T) {
		f := &Function{ObjectMeta: meta}
		f.Spec.InvokeStrategy = InvokeStrategy{StrategyType: StrategyTypeExecution, ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr}}
		require.NoError(t, f.Validate())

		bad := &Function{ObjectMeta: metav1.ObjectMeta{Name: "Bad_Name"}}
		require.Error(t, bad.Validate())
	})

	t.Run("package", func(t *testing.T) {
		p := &Package{ObjectMeta: meta}
		p.Spec.Environment = EnvironmentReference{Name: "env"}
		p.Status.BuildStatus = BuildStatusSucceeded
		require.NoError(t, p.Validate())
	})

	t.Run("environment", func(t *testing.T) {
		e := &Environment{ObjectMeta: meta}
		e.Spec.Version = 2
		require.NoError(t, e.Validate())
	})

	t.Run("httptrigger", func(t *testing.T) {
		h := &HTTPTrigger{ObjectMeta: meta}
		h.Spec.FunctionReference = FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "hello"}
		require.NoError(t, h.Validate())
	})
}

func TestListValidate(t *testing.T) {
	t.Parallel()
	good := metav1.ObjectMeta{Name: "ok", Namespace: "default"}
	bad := metav1.ObjectMeta{Name: "Bad_Name"}

	fl := &FunctionList{Items: []Function{{ObjectMeta: good}, {ObjectMeta: bad}}}
	require.Error(t, fl.Validate(), "list propagates an invalid item's error")

	pl := &PackageList{Items: []Package{{
		ObjectMeta: good,
		Spec:       PackageSpec{Environment: EnvironmentReference{Name: "env"}},
		Status:     PackageStatus{BuildStatus: BuildStatusSucceeded},
	}}}
	require.NoError(t, pl.Validate())
}
