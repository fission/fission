// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// TestFetcherSigningNamespace pins the version-aware signing decision: the
// executor must sign each /specialize call with the key the target pod's fetcher
// actually verifies with, or the specialization 401s. A pod stamped with the
// namespace key-scheme annotation (created while dynamic tenancy was on for its
// namespace) holds only its per-namespace key; every other pod — pre-upgrade
// pods with no annotation, or any pod when tenancy is off — verifies with the
// master-derived key. Getting this wrong is a cross-tenant 401 storm (or, worse,
// a missed isolation boundary), so it is exercised directly.
func TestFetcherSigningNamespace(t *testing.T) {
	const podNS = "team-a"
	nsPod := &apiv1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace:   podNS,
		Annotations: map[string]string{fv1.AuthKeySchemeAnnotation: fv1.AuthKeySchemeNamespace},
	}}
	plainPod := &apiv1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: podNS}}

	tests := []struct {
		name       string
		dynamic    bool
		pod        *apiv1.Pod
		wantNS     string
		wantScoped bool
	}{
		{"dynamic on, ns-scheme pod signs with that namespace's key", true, nsPod, podNS, true},
		{"dynamic on, pre-upgrade pod (no annotation) stays master-signed", true, plainPod, "", false},
		{"dynamic off ignores a stale annotation and stays master-signed", false, nsPod, "", false},
		{"dynamic off, plain pod stays master-signed", false, plainPod, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FISSION_TENANCY_MODE", tenancyModeEnv(tt.dynamic))
			ns, scoped := fetcherSigningNamespace(tt.pod)
			assert.Equal(t, tt.wantScoped, scoped, "nsScoped decision")
			assert.Equal(t, tt.wantNS, ns, "signing namespace")
		})
	}
}

// TestShouldStampNamespaceKeyScheme pins the other half of the version-aware
// contract: the executor stamps the namespace key-scheme annotation onto a pool
// pod only when dynamic tenancy is on AND the pod's namespace is a live tenant —
// i.e. the tenant controller has already provisioned that namespace's derived-key
// Secret, so the pod will actually mount the per-namespace key it is being
// promised. A stamp without a matching key would 401 every specialization.
func TestShouldStampNamespaceKeyScheme(t *testing.T) {
	resolver := &utils.NamespaceResolver{}
	resolver.AddTenant("team-a")

	tests := []struct {
		name      string
		dynamic   bool
		namespace string
		resolver  *utils.NamespaceResolver
		want      bool
	}{
		{"dynamic on + tenant namespace stamps", true, "team-a", resolver, true},
		{"dynamic on + non-tenant namespace does not stamp", true, "other", resolver, false},
		{"dynamic off never stamps", false, "team-a", resolver, false},
		{"nil resolver never stamps", true, "team-a", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FISSION_TENANCY_MODE", tenancyModeEnv(tt.dynamic))
			assert.Equal(t, tt.want, shouldStampNamespaceKeyScheme(tt.namespace, tt.resolver))
		})
	}
}

// tenancyModeEnv maps the test's "dynamic?" flag to a FISSION_TENANCY_MODE value.
func tenancyModeEnv(dynamic bool) string {
	if dynamic {
		return "dynamic"
	}
	return "static"
}

// TestExistsInFnNamespace pins the cold-path pre-flight check that confirms a
// function's Secrets/ConfigMaps live in the function namespace: it must serve
// from the executor Manager cache (crClient) when possible, but fall back to a
// direct API read on a cache miss so a just-created object the informer hasn't
// observed yet is not spuriously reported missing.
func TestExistsInFnNamespace(t *testing.T) {
	const ns = "fn-ns"
	configMap := func(name string) *apiv1.ConfigMap {
		return &apiv1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	}
	// newGP builds a pool whose cache holds cacheObjs and whose API client holds
	// apiObjs, so a test can place an object in only one of the two.
	newGP := func(cacheObjs []client.Object, apiObjs ...*apiv1.ConfigMap) *GenericPool {
		k8sObjs := make([]runtime.Object, 0, len(apiObjs))
		for _, o := range apiObjs {
			k8sObjs = append(k8sObjs, o)
		}
		return &GenericPool{
			logger:           logr.Discard(),
			fnNamespace:      ns,
			crClient:         crfake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(cacheObjs...).Build(),
			kubernetesClient: k8sfake.NewSimpleClientset(k8sObjs...),
		}
	}
	directGet := func(gp *GenericPool, name string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, e := gp.kubernetesClient.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
			return e
		}
	}

	t.Run("served from cache without touching the API", func(t *testing.T) {
		// Present only in the cache: a cache hit must short-circuit the API read.
		gp := newGP([]client.Object{configMap("in-cache")})
		exists, err := gp.existsInFnNamespace(t.Context(), &apiv1.ConfigMap{}, "in-cache", directGet(gp, "in-cache"))
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("cache miss falls back to a direct read", func(t *testing.T) {
		// Present only in the API client (not yet observed by the informer): the
		// fallback must confirm it rather than report it missing.
		gp := newGP(nil, configMap("only-in-api"))
		exists, err := gp.existsInFnNamespace(t.Context(), &apiv1.ConfigMap{}, "only-in-api", directGet(gp, "only-in-api"))
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("absent in both reports not found", func(t *testing.T) {
		gp := newGP(nil)
		exists, err := gp.existsInFnNamespace(t.Context(), &apiv1.ConfigMap{}, "ghost", directGet(gp, "ghost"))
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("a non-not-found read error is propagated", func(t *testing.T) {
		gp := newGP(nil)
		boom := errors.New("apiserver unavailable")
		exists, err := gp.existsInFnNamespace(t.Context(), &apiv1.ConfigMap{}, "x",
			func(context.Context) error { return boom })
		require.ErrorIs(t, err, boom)
		assert.False(t, exists)
	})
}
