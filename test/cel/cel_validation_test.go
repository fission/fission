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
// admission webhook (pkg/webhook) and validation.go. The cases here cover the
// field-level CEL rules that DO compile and stay under the cost budget:
// enum/range scalar rules, SSA immutability, and the reference-name DNS-1123
// patterns (env/secret/configmap/package names + watch-trigger namespace) that
// moved from the webhook to the API server.
//
// These tests need the envtest control-plane binaries; they skip cleanly when
// KUBEBUILDER_ASSETS is unset (e.g. a plain `go test ./...` without the
// harness). `make test-run` sets it. The envtest control plane is started once
// per package (TestMain) and the client is reused across cases; every test
// object uses a unique name so a shared API server stays isolated.
package cel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

const ns = "default"

// fissionClient is the shared client against the single envtest API server
// started by TestMain. It is nil when KUBEBUILDER_ASSETS is unset.
var fissionClient versioned.Interface

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// envtest binaries unavailable; each test skips individually.
		os.Exit(m.Run())
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
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest start failed (CRDs did not install — check CEL rules): %v\n", err)
		os.Exit(1)
	}

	fc, err := crd.NewClientGeneratorWithRestConfig(cfg).GetFissionClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fission client: %v\n", err)
		_ = env.Stop()
		os.Exit(1)
	}
	fissionClient = fc

	code := m.Run()

	if err := env.Stop(); err != nil {
		// Surface (don't hide) shutdown failures so leaked envtest
		// processes/ports are diagnosable in CI logs.
		fmt.Fprintf(os.Stderr, "envtest stop failed: %v\n", err)
	}
	os.Exit(code)
}

func client(t *testing.T) versioned.Interface {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `make test-run` or set it with setup-envtest")
	}
	return fissionClient
}

func TestCELFunctionValidation(t *testing.T) {
	fc := client(t)

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
	fc := client(t)

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
	fc := client(t)

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

// TestCELReferenceNamePatterns proves the API server now enforces the
// reference-name DNS-1123 patterns that moved out of the webhook: env / secret
// / configmap / package names. The optional package ref is skipped when empty.
func TestCELReferenceNamePatterns(t *testing.T) {
	fc := client(t)

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
		wantErr bool
	}{
		{"valid secret + configmap names", fn("ref-valid", func(f *fv1.Function) {
			f.Spec.Secrets = []fv1.SecretReference{{Name: "my-secret", Namespace: ns}}
			f.Spec.ConfigMaps = []fv1.ConfigMapReference{{Name: "my-config", Namespace: ns}}
		}), false},
		{"empty package ref accepted", fn("ref-nopkg", nil), false},
		{"bad environment name", fn("ref-bad-env", func(f *fv1.Function) {
			f.Spec.Environment.Name = "Bad_Env"
		}), true},
		{"bad secret name", fn("ref-bad-secret", func(f *fv1.Function) {
			f.Spec.Secrets = []fv1.SecretReference{{Name: "Bad_Secret", Namespace: ns}}
		}), true},
		{"bad configmap name", fn("ref-bad-cm", func(f *fv1.Function) {
			f.Spec.ConfigMaps = []fv1.ConfigMapReference{{Name: "Bad_CM", Namespace: ns}}
		}), true},
		{"bad package ref name", fn("ref-bad-pkg", func(f *fv1.Function) {
			f.Spec.Package = fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Name: "Bad_Pkg"}}
		}), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fc.CoreV1().Functions(ns).Create(t.Context(), tc.fn, metav1.CreateOptions{})
			if tc.wantErr {
				require.Error(t, err, "apiserver should reject invalid reference name")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestCELPackageEnvironmentName(t *testing.T) {
	fc := client(t)
	pkg := func(name, envName string) *fv1.Package {
		return &fv1.Package{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       fv1.PackageSpec{Environment: fv1.EnvironmentReference{Name: envName, Namespace: ns}},
		}
	}
	_, err := fc.CoreV1().Packages(ns).Create(t.Context(), pkg("pkg-env-ok", "env"), metav1.CreateOptions{})
	assert.NoError(t, err)
	_, err = fc.CoreV1().Packages(ns).Create(t.Context(), pkg("pkg-env-bad", "Bad_Env"), metav1.CreateOptions{})
	require.Error(t, err, "apiserver should reject invalid environment name on Package")
}

// TestCELKubernetesWatchTriggerNamespace covers the spec.namespace required +
// DNS-1123 pattern (relocated from the webhook's empty-namespace assertion).
func TestCELKubernetesWatchTriggerNamespace(t *testing.T) {
	fc := client(t)
	kwt := func(name, specNs string) *fv1.KubernetesWatchTrigger {
		return &fv1.KubernetesWatchTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: fv1.KubernetesWatchTriggerSpec{
				Namespace:         specNs,
				Type:              "POD",
				FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
			},
		}
	}
	cases := []struct {
		objName string
		specNs  string
		wantErr bool
	}{
		{"kwt-ns-ok", "default", false},
		{"kwt-ns-empty", "", true},
		{"kwt-ns-bad", "Bad_NS", true},
	}
	for _, tc := range cases {
		t.Run(tc.objName, func(t *testing.T) {
			_, err := fc.CoreV1().KubernetesWatchTriggers(ns).Create(t.Context(), kwt(tc.objName, tc.specNs), metav1.CreateOptions{})
			if tc.wantErr {
				require.Error(t, err, "apiserver should reject invalid/empty spec.namespace")
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestCELFunctionVersionAndAliasInstall is the RFC-0025 early CEL-cost check
// (Task 1, Step 3b): FunctionVersionSpec embeds a full FunctionSpec one level
// deeper than Function itself (Function.spec vs FunctionVersion.spec.snapshot),
// carrying every one of FunctionSpec's XValidation rules and its PodSpec along
// for the ride. TestMain's envtest.Environment.Start already proves both new
// CRDs installed cleanly (an over-budget or non-compiling
// x-kubernetes-validations rule fails CRD apply, which fails Start, which
// os.Exit(1)s before any test runs) — this test additionally exercises actual
// Create calls against both types, including the FunctionAliasSpec CEL rules,
// so a schema that "installs" but silently drops/miscompiles a rule is still
// caught.
func TestCELFunctionVersionAndAliasInstall(t *testing.T) {
	fc := client(t)

	fv := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "fv-valid", Namespace: ns},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:       "fn",
			FunctionUID:        apitypes.UID("fn-uid"),
			FunctionGeneration: 1,
			Sequence:           1,
			Snapshot: fv1.FunctionSpec{
				Environment:    fv1.EnvironmentReference{Name: "env", Namespace: ns},
				InvokeStrategy: fv1.InvokeStrategy{ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypePoolmgr}},
			},
			PackageDigest: "sha256:" + strings.Repeat("a", 64),
			PublishedAt:   metav1.Now(),
		},
	}
	_, err := fc.CoreV1().FunctionVersions(ns).Create(t.Context(), fv, metav1.CreateOptions{})
	require.NoError(t, err, "valid FunctionVersion (embedded FunctionSpec) should be accepted")

	alias := func(name string, mut func(*fv1.FunctionAlias)) *fv1.FunctionAlias {
		a := &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fv-valid"},
		}
		if mut != nil {
			mut(a)
		}
		return a
	}

	weight := 50
	cases := []struct {
		name    string
		alias   *fv1.FunctionAlias
		wantErr string // empty => expect accept
	}{
		{"valid version-pinned", alias("fa-valid", nil), ""},
		{"valid digest-pinned", alias("fa-digest", func(a *fv1.FunctionAlias) {
			a.Spec.Version = ""
			a.Spec.PackageDigest = "sha256:" + strings.Repeat("b", 64)
		}), ""},
		{"valid weighted rollout", alias("fa-weighted", func(a *fv1.FunctionAlias) {
			a.Spec.Weight = &weight
			a.Spec.SecondaryVersion = "fv-other"
		}), ""},
		{"neither version nor digest set", alias("fa-neither", func(a *fv1.FunctionAlias) {
			a.Spec.Version = ""
		}), "exactly one of version and packageDigest"},
		{"both version and digest set", alias("fa-both", func(a *fv1.FunctionAlias) {
			a.Spec.PackageDigest = "sha256:" + strings.Repeat("c", 64)
		}), "exactly one of version and packageDigest"},
		{"weight without secondaryVersion", alias("fa-weight-nosecondary", func(a *fv1.FunctionAlias) {
			a.Spec.Weight = &weight
		}), "weight requires secondaryVersion"},
		{"secondaryVersion equals version", alias("fa-secondary-same", func(a *fv1.FunctionAlias) {
			a.Spec.Weight = &weight
			a.Spec.SecondaryVersion = "fv-valid"
		}), "secondaryVersion must differ from version"},
		{"bad packageDigest pattern", alias("fa-bad-digest", func(a *fv1.FunctionAlias) {
			a.Spec.Version = ""
			a.Spec.PackageDigest = "not-a-digest"
		}), "packageDigest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fc.CoreV1().FunctionAliases(ns).Create(t.Context(), tc.alias, metav1.CreateOptions{})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err, "apiserver should reject")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	// Function.Spec.Versioning.Retain has a GC floor of 1 (CRD Minimum=1):
	// a Function cannot opt into retaining zero unaliased versions.
	retainFn := func(name string, retain int) *fv1.Function {
		return &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: fv1.FunctionSpec{
				Environment:    fv1.EnvironmentReference{Name: "env", Namespace: ns},
				InvokeStrategy: fv1.InvokeStrategy{ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypePoolmgr}},
				Versioning:     &fv1.VersioningConfig{Retain: &retain},
			},
		}
	}

	retainCases := []struct {
		name    string
		fn      *fv1.Function
		wantErr string // empty => expect accept
	}{
		{"retain below minimum rejected", retainFn("f-retain-zero", 0), "retain"},
		{"retain at valid value accepted", retainFn("f-retain-ten", 10), ""},
	}

	for _, tc := range retainCases {
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

func TestCELHTTPTriggerRouteConfig(t *testing.T) {
	fc := client(t)

	ht := func(name string, mut func(*fv1.HTTPTrigger)) *fv1.HTTPTrigger {
		h := &fv1.HTTPTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: fv1.HTTPTriggerSpec{
				RelativeURL:       "/" + name,
				Methods:           []string{"GET"},
				FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
				RouteConfig: &fv1.RouteConfig{
					Provider: fv1.RouteProviderGateway,
					Gateway:  &fv1.GatewayRouteConfig{ParentRefs: []fv1.GatewayParentRef{{Name: "eg"}}},
				},
			},
		}
		if mut != nil {
			mut(h)
		}
		return h
	}

	cases := []struct {
		name    string
		ht      *fv1.HTTPTrigger
		wantErr string // empty => expect accept
	}{
		{"valid gateway with parentRef", ht("rc-valid", nil), ""},
		{"valid ingress provider", ht("rc-ingress", func(h *fv1.HTTPTrigger) {
			h.Spec.RouteConfig = &fv1.RouteConfig{Provider: fv1.RouteProviderIngress}
		}), ""},
		{"gateway without parentRefs", ht("rc-noparent", func(h *fv1.HTTPTrigger) {
			h.Spec.RouteConfig.Gateway = nil
		}), "parentRefs"},
		{"unknown provider", ht("rc-badprovider", func(h *fv1.HTTPTrigger) {
			h.Spec.RouteConfig.Provider = fv1.RouteProviderType("nginx")
		}), "provider"},
		{"non-absolute path", ht("rc-badpath", func(h *fv1.HTTPTrigger) {
			h.Spec.RouteConfig.Path = "no-slash"
		}), "path"},
		{"tls with gateway provider", ht("rc-gwtls", func(h *fv1.HTTPTrigger) {
			h.Spec.RouteConfig.TLS = "some-secret"
		}), "tls"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fc.CoreV1().HTTPTriggers(ns).Create(t.Context(), tc.ht, metav1.CreateOptions{})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err, "apiserver should reject")
			if !strings.Contains(err.Error(), tc.wantErr) {
				assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tc.wantErr))
			}
		})
	}
}
