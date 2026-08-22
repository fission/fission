// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	asv2 "k8s.io/api/autoscaling/v2"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// compat gate for the json/v2 migration — cross-version RPC wire (rolling-upgrade
// skew; builder images and pool pods outlive releases); if this fails after a
// migration commit, add compat options at the marshal site, do not update the
// fixture.
//
// GetServiceForFunction / EnsureCapacity (pkg/executor/client/client.go) marshal
// a full *fv1.Function as the RPC request body sent to the executor. fv1's CRD
// types have several non-pointer struct fields tagged `omitempty` (e.g.
// FunctionStatus, RetryPolicy) that v1's json.Marshal always emits even when
// every field of the struct is zero, and several slice fields with no
// `omitempty` at all (e.g. apiv1.PodSpec.Containers, reachable through
// FunctionSpec.PodSpec) that v1 emits as JSON null when nil. encoding/json/v2's
// default omitempty semantics treat a zero-valued struct as empty (it would be
// OMITTED) and its default nil-slice behavior can differ from v1's `null` — a
// silent wire-format change an old executor/builder/router on the other side of
// a rolling upgrade would not tolerate. These tests pin today's exact v1 output
// so a migration that changes it fails loudly here instead of in production.

// richFunction returns a *fv1.Function with every optional section populated,
// including the InvocationConfig.Retry sub-struct left at its all-pointers-nil
// zero value to exercise the same struct-omitempty risk one level deeper.
func richFunction() *fv1.Function {
	idle := 120
	maxAge := metav1.Duration{Duration: 300 * time.Second}
	return &fv1.Function{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Function",
			APIVersion: "fission.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "rich-fn",
			Namespace:       "test-ns",
			ResourceVersion: "12345",
			UID:             types.UID("11111111-1111-1111-1111-111111111111"),
			Labels: map[string]string{
				"app": "demo",
				"env": "prod",
			},
			Annotations: map[string]string{
				"note": "rich fixture",
			},
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{
				Namespace: "test-ns",
				Name:      "nodejs",
			},
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{
					Namespace:       "test-ns",
					Name:            "rich-pkg",
					ResourceVersion: "1",
				},
				FunctionName: "handler",
			},
			Secrets: []fv1.SecretReference{
				{Namespace: "test-ns", Name: "sec1", MountPath: "sec1"},
				{Namespace: "test-ns", Name: "sec2"},
			},
			ConfigMaps: []fv1.ConfigMapReference{
				{Namespace: "test-ns", Name: "cm1"},
			},
			Env: []apiv1.EnvVar{
				{Name: "FOO", Value: "bar"},
			},
			Resources: apiv1.ResourceRequirements{
				Limits: apiv1.ResourceList{
					apiv1.ResourceCPU:    resource.MustParse("200m"),
					apiv1.ResourceMemory: resource.MustParse("256Mi"),
				},
				Requests: apiv1.ResourceList{
					apiv1.ResourceCPU:    resource.MustParse("100m"),
					apiv1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypePoolmgr,
					MinScale:              1,
					MaxScale:              3,
					SpecializationTimeout: 120,
					Metrics: []asv2.MetricSpec{
						{Type: asv2.ResourceMetricSourceType},
					},
				},
				StrategyType: fv1.StrategyTypeExecution,
			},
			FunctionTimeout: 60,
			IdleTimeout:     &idle,
			Streaming: &fv1.StreamingConfig{
				Protocol:           fv1.StreamingSSE,
				IdleTimeoutSeconds: 30,
			},
			Concurrency:    500,
			RequestsPerPod: 1,
			Invocation: &fv1.InvocationConfig{
				Retry:  fv1.RetryPolicy{},
				MaxAge: &maxAge,
			},
		},
		Status: fv1.FunctionStatus{
			ObservedGeneration:    3,
			ProvisionedReady:      2,
			ProvisionedTarget:     3,
			ProvisionedSpecTarget: 5,
		},
	}
}

// zeroHeavyFunction returns a *fv1.Function with only Name/Namespace set and
// every other field left at its Go zero value. This pins two v1 behaviors a
// naive json/v2 migration would silently change: FunctionStatus (a non-pointer
// struct tagged `status,omitempty`) is still emitted as `"status":{}` even
// though every one of its fields is zero, and FunctionSpec.Resources (tagged
// `resources` with no omitempty at all) is emitted as `"resources":{}`.
func zeroHeavyFunction() *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "zero-fn",
			Namespace: "default",
		},
	}
}

// readGolden loads a testdata fixture and trims a single trailing newline, so
// the file can be authored as an ordinary text file (ending in \n) while the
// comparison is against json.Marshal's exact, newline-free output.
func readGolden(t *testing.T, path string) []byte {
	t.Helper()
	golden, err := os.ReadFile(path)
	require.NoError(t, err)
	return bytes.TrimSuffix(golden, []byte("\n"))
}

func TestFunctionJSONWireCompat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fn     *fv1.Function
		golden string
	}{
		{"rich", richFunction(), "testdata/function_rich.json"},
		{"zero_heavy", zeroHeavyFunction(), "testdata/function_zero_heavy.json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			golden := readGolden(t, tc.golden)

			body, err := json.Marshal(tc.fn)
			require.NoError(t, err)
			require.Equal(t, string(golden), string(body),
				"compat gate for the json/v2 migration — cross-version RPC wire "+
					"(rolling-upgrade skew; builder images and pool pods outlive "+
					"releases); if this fails after a migration commit, add compat "+
					"options at the marshal site, do not update the fixture")

			var decoded fv1.Function
			require.NoError(t, json.Unmarshal(golden, &decoded))
			assert.Equal(t, tc.fn, &decoded, "decoding the golden fixture back into "+
				"fv1.Function must reproduce the fixture struct exactly")
		})
	}
}

// TestFunctionMarshal_PodSpecNilContainersEmitsNull pins the other half of the
// json/v2 struct-omitempty risk: a slice field with NO omitempty tag at all
// (apiv1.PodSpec.Containers, reachable through FunctionSpec.PodSpec for a
// container-executor Function) marshals a nil value as JSON `null` under v1.
//
// compat gate for the json/v2 migration — cross-version RPC wire (rolling-upgrade
// skew; builder images and pool pods outlive releases); if this fails after a
// migration commit, add compat options at the marshal site, do not update the
// fixture.
func TestFunctionMarshal_PodSpecNilContainersEmitsNull(t *testing.T) {
	t.Parallel()

	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "container-fn", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			PodSpec: &apiv1.PodSpec{},
		},
	}

	body, err := json.Marshal(fn)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"containers":null`,
		"a nil apiv1.PodSpec.Containers (no omitempty tag) must marshal as null, "+
			"not an omitted key or an empty array")

	var decoded fv1.Function
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, fn, &decoded)
}
