// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package cel verifies that the CRD-level CEL (x-kubernetes-validations) and
// server-side-apply markers added in pkg/apis/core/v1/types.go are enforced by
// the Kubernetes API server itself.
//
// Scope note: the cross-namespace and podspec/container security checks (the
// GHSA fixes) are NOT expressible as CRD CEL — cross-namespace rules need
// metadata.namespace (not exposed to CRD CEL) and the embedded PodSpec list
// rules blow the apiserver CEL cost budget. Those remain enforced by the
// admission webhook (pkg/webhook) and validation.go. The cases here only cover
// the field-level CEL rules that DO compile and stay under the cost budget:
// enum/range scalar rules and SSA immutability.
//
// These tests need the envtest control-plane binaries; they skip cleanly when
// KUBEBUILDER_ASSETS is unset (e.g. a plain `go test ./...` without the
// harness). `make test-run` sets it.
package cel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

const ns = "default"

func startEnv(t *testing.T) versioned.Interface {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `make test-run` or set it with setup-envtest")
	}
	ctrllog.SetLogger(logr.Discard())

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "crds", "v1")},
		ErrorIfCRDPathMissing: true,
		CRDInstallOptions:     envtest.CRDInstallOptions{MaxTime: 60 * time.Second},
		BinaryAssetsDirectory: os.Getenv("KUBEBUILDER_ASSETS"),
	}
	// A successful Start() means every CRD — with all its CEL rules — installed
	// cleanly, which is itself the apiserver CEL cost-budget proof: an
	// over-budget or non-compiling x-kubernetes-validations rule is rejected at
	// CRD apply time.
	cfg, err := env.Start()
	require.NoError(t, err, "envtest start (also proves CEL cost-budget: CRDs installed)")
	t.Cleanup(func() { _ = env.Stop() })

	fc, err := crd.NewClientGeneratorWithRestConfig(cfg).GetFissionClient()
	require.NoError(t, err)
	return fc
}

func TestCELFunctionValidation(t *testing.T) {
	fc := startEnv(t)

	fn := func(name string, mut func(*fv1.Function)) *fv1.Function {
		f := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: fv1.FunctionSpec{
				Environment:    fv1.EnvironmentReference{Name: "env", Namespace: ns},
				InvokeStrategy: fv1.InvokeStrategy{ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypePoolmgr}},
			},
		}
		if mut != nil {
			mut(f)
		}
		return f
	}

	cases := []struct {
		name    string
		fn      *fv1.Function
		wantErr string // empty => expect accept
	}{
		{"valid poolmgr", fn("f-valid", nil), ""},
		{"invalid executor type enum", fn("f-bad-executor", func(f *fv1.Function) {
			f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorType("bogus")
		}), "ExecutorType"},
		{"executor container requires podspec", fn("f-container-nopodspec", func(f *fv1.Function) {
			f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeContainer
		}), "pod spec"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fc.CoreV1().Functions(ns).Create(t.Context(), tc.fn, metav1.CreateOptions{})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err, "apiserver should reject")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestCELEnvironmentValidation(t *testing.T) {
	fc := startEnv(t)

	env := func(name string, mut func(*fv1.Environment)) *fv1.Environment {
		e := &fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       fv1.EnvironmentSpec{Version: 1, Runtime: fv1.Runtime{Image: "img"}},
		}
		if mut != nil {
			mut(e)
		}
		return e
	}

	cases := []struct {
		name    string
		env     *fv1.Environment
		wantErr string // empty => expect accept
	}{
		{"valid v1", env("e-valid", nil), ""},
		{"version above maximum", env("e-ver-high", func(e *fv1.Environment) { e.Spec.Version = 4 }), "spec.version"},
		{"version below minimum", env("e-ver-low", func(e *fv1.Environment) { e.Spec.Version = 0 }), "spec.version"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fc.CoreV1().Environments(ns).Create(t.Context(), tc.env, metav1.CreateOptions{})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			// version range message comes from the structural schema (Minimum/Maximum);
			// accept either the field path or the lowercased message text.
			if !strings.Contains(err.Error(), tc.wantErr) {
				assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tc.wantErr))
			}
		})
	}
}

func TestCELEnvironmentVersionImmutable(t *testing.T) {
	fc := startEnv(t)

	created, err := fc.CoreV1().Environments(ns).Create(t.Context(),
		&fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{Name: "e-immutable", Namespace: ns},
			Spec:       fv1.EnvironmentSpec{Version: 1, Runtime: fv1.Runtime{Image: "img"}},
		}, metav1.CreateOptions{})
	require.NoError(t, err)

	created.Spec.Version = 2
	_, err = fc.CoreV1().Environments(ns).Update(t.Context(), created, metav1.UpdateOptions{})
	require.Error(t, err, "spec.version should be immutable")
	assert.Contains(t, err.Error(), "immutable")
}
