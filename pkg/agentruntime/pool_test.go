// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/utils"
)

// --- checkPoolRBAC ---

// alwaysAllowSAR wires kubeClient's SelfSubjectAccessReview Create to always
// report Allowed=true. The fake clientset's default Create returns the
// object with a ZERO-VALUE Status (Allowed=false), so any allowed-path test
// needs this reactor — a bare fake.NewClientset() only ever exercises the
// denied path.
func alwaysAllowSAR(kubeClient *fake.Clientset) {
	kubeClient.PrependReactor("create", "selfsubjectaccessreviews",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			// SAFETY: this reactor is registered for "create" on selfsubjectaccessreviews, so the action is a CreateAction carrying a *SelfSubjectAccessReview.
			sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
			return true, &authorizationv1.SelfSubjectAccessReview{
				Spec:   sar.Spec,
				Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
			}, nil
		})
}

func TestCheckPoolRBACAllowed(t *testing.T) {
	t.Parallel()
	kubeClient := fake.NewClientset()
	alwaysAllowSAR(kubeClient)

	assert.NoError(t, checkPoolRBAC(t.Context(), kubeClient))
}

func TestCheckPoolRBACDenied(t *testing.T) {
	t.Parallel()
	// A bare fake clientset's Create fails at the fake tracker's own object
	// conversion (no SelfSubjectAccessReview handler registered for typed
	// apply), not with an explicit Allowed=false — so, like the allowed-path
	// test, this needs a reactor: one that returns a real, explicitly denied
	// SubjectAccessReviewStatus.
	kubeClient := fake.NewClientset()
	kubeClient.PrependReactor("create", "selfsubjectaccessreviews",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			// SAFETY: this reactor is registered for "create" on selfsubjectaccessreviews, so the action is a CreateAction carrying a *SelfSubjectAccessReview.
			sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
			return true, &authorizationv1.SelfSubjectAccessReview{
				Spec:   sar.Spec,
				Status: authorizationv1.SubjectAccessReviewStatus{Allowed: false, Reason: "no grant"},
			}, nil
		})

	err := checkPoolRBAC(t.Context(), kubeClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed to")
}

// --- poolCacheOptions ---

// byObjectFor finds opts.ByObject's entry for a client.Object of type T.
// ByObject is keyed by interface value (pointer identity of a fresh &T{}),
// so a literal map lookup can never find an existing entry — callers must
// search by dynamic type instead.
func byObjectFor[T client.Object](t *testing.T, opts crcache.Options) crcache.ByObject {
	t.Helper()
	for k, v := range opts.ByObject {
		if _, ok := k.(T); ok {
			return v
		}
	}
	t.Fatalf("no ByObject entry found for type %T", *new(T))
	return crcache.ByObject{}
}

func TestPoolCacheOptionsPodSelectorMatchesAnyExecutorType(t *testing.T) {
	t.Parallel()
	opts, err := poolCacheOptions(crcache.Options{})
	require.NoError(t, err)
	byObj := byObjectFor[*corev1.Pod](t, opts)

	// A pool warm pod (EXECUTOR_TYPE=poolmgr) and a container-executor pod
	// (EXECUTOR_TYPE=container) both match — Exists, not a value equality —
	// but a pod carrying no EXECUTOR_TYPE label at all (e.g. an unrelated
	// workload) does not.
	assert.True(t, byObj.Label.Matches(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr)}))
	assert.True(t, byObj.Label.Matches(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypeContainer)}))
	assert.False(t, byObj.Label.Matches(labels.Set{"unrelated": "true"}))

	// Confirms the pool.go blocker-fix deviation from a literal
	// MANAGED_BY_LABEL filter: that label is never set on a Pod in
	// production (only on Services/EndpointSlices), so it must NOT be what
	// gates Pod visibility — a pod carrying only that label (which cannot
	// happen for a real pod, but pins the selector's actual behavior) must
	// still not match.
	assert.False(t, byObj.Label.Matches(labels.Set{fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE}))
}

func TestPoolCacheOptionsSliceSelectorIsManagedByLabel(t *testing.T) {
	t.Parallel()
	opts, err := poolCacheOptions(crcache.Options{})
	require.NoError(t, err)
	byObj := byObjectFor[*discoveryv1.EndpointSlice](t, opts)

	assert.True(t, byObj.Label.Matches(labels.Set{fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE}))
	assert.False(t, byObj.Label.Matches(labels.Set{fv1.MANAGED_BY_LABEL: "something-else"}))
}

func TestPoolCacheOptionsNamespaceScoping(t *testing.T) {
	r := utils.DefaultNSResolver()
	orig := r.FissionResourceNamespaces()
	t.Cleanup(func() { r.SetTenants(orig) })

	t.Run("static mode scopes to function namespaces", func(t *testing.T) {
		t.Setenv("FISSION_TENANCY_MODE", "static")
		r.SetTenants(map[string]string{"ns-a": "ns-a"})

		opts, err := poolCacheOptions(crcache.Options{})
		require.NoError(t, err)
		podObj := byObjectFor[*corev1.Pod](t, opts)
		sliceObj := byObjectFor[*discoveryv1.EndpointSlice](t, opts)
		assert.Contains(t, podObj.Namespaces, "ns-a")
		assert.Contains(t, sliceObj.Namespaces, "ns-a")
	})

	t.Run("cluster tenancy watches cluster-wide", func(t *testing.T) {
		t.Setenv("FISSION_TENANCY_MODE", "cluster")
		r.SetTenants(map[string]string{"ns-a": "ns-a"})

		opts, err := poolCacheOptions(crcache.Options{})
		require.NoError(t, err)
		podObj := byObjectFor[*corev1.Pod](t, opts)
		sliceObj := byObjectFor[*discoveryv1.EndpointSlice](t, opts)
		assert.Empty(t, podObj.Namespaces, "cluster tenancy must not scope the pod watch to specific namespaces")
		assert.Empty(t, sliceObj.Namespaces, "cluster tenancy must not scope the slice watch to specific namespaces")
	})
}

// --- PoolAPI.ServePool ---

// poolTestPod builds a Pod fixture with the real label sets poolmgr sets:
// served distinguishes a specialized/served pod (function labels, served,
// generation) from a warm-unspecialized pool pod (env labels only, no
// function/served/generation labels at all — see gp_pod.go).
func poolTestPod(ns, name, ip string, served bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				fv1.EXECUTOR_TYPE:    string(fv1.ExecutorTypePoolmgr),
				fv1.ENVIRONMENT_NAME: "node",
				"managed":            "true",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: ip,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	if served {
		p.Labels["managed"] = "false"
		p.Labels[fv1.FUNCTION_NAME] = "fn"
		p.Labels[fv1.FUNCTION_GENERATION] = "3"
		p.Labels[fv1.SERVED_LABEL] = fv1.SERVED_VALUE
	}
	return p
}

// newTestPoolAPI wires a PoolAPI + its mux exactly as main.go mounts it (GET
// /registry/pool behind authz.HTTPMiddleware only — no {namespace} path
// value). objs seed the fake Manager-cache client (Pod reads); index and
// view default to empty when nil.
func newTestPoolAPI(t *testing.T, authz *Authorizer, synced, degraded bool, index *endpointcache.Index, view *AgentView, objs ...client.Object) http.Handler {
	t.Helper()
	c := crfake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objs...).Build()
	if view == nil {
		view = NewAgentView()
	}
	api := NewPoolAPI(c, index, view, authz, func() bool { return synced }, func() bool { return degraded })
	mux := http.NewServeMux()
	mux.Handle("GET /registry/pool", authz.HTTPMiddleware(http.HandlerFunc(api.ServePool)))
	return mux
}

func TestServePoolDegradedReturns503(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux := newTestPoolAPI(t, authz, true /* synced */, true /* degraded */, nil, nil)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/registry/pool", nil))

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "missing RBAC (endpointslices/pods)")
}

func TestServePoolWarmingReturns503(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux := newTestPoolAPI(t, authz, false /* synced */, false /* degraded */, nil, nil)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/registry/pool", nil))

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "pool cache warming")
}

// TestServePoolListsWarmAndServedPods pins the brief's core fixture: a warm,
// unspecialized pool pod (no function/served labels — it is in NO
// EndpointSlice since no function Service selects it yet) and a served,
// specialized pod must BOTH appear, with their real labels surfaced.
func TestServePoolListsWarmAndServedPods(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	warm := poolTestPod("ns", "warm-pod", "10.0.0.1", false)
	served := poolTestPod("ns", "served-pod", "10.0.0.2", true)
	mux := newTestPoolAPI(t, authz, true, false, nil, nil, warm, served)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/registry/pool", nil))
	require.Equal(t, http.StatusOK, w.Code)

	body := decodeJSON[poolResponse](t, w)
	require.Len(t, body.Pods, 2)

	byName := map[string]PodSummary{}
	for _, p := range body.Pods {
		byName[p.Name] = p
	}

	gotWarm, ok := byName["warm-pod"]
	require.True(t, ok)
	assert.False(t, gotWarm.Served)
	assert.Empty(t, gotWarm.FunctionGeneration)
	assert.Equal(t, "node", gotWarm.Environment)
	assert.Equal(t, "10.0.0.1", gotWarm.IP)
	assert.True(t, gotWarm.Ready)

	gotServed, ok := byName["served-pod"]
	require.True(t, ok)
	assert.True(t, gotServed.Served)
	assert.Equal(t, "3", gotServed.FunctionGeneration)
	assert.Equal(t, "10.0.0.2", gotServed.IP)
}

// TestServePoolEndpointsBreakdown pins the "endpoints" half of the response:
// keyed "<namespace>/<name>" for agent-enabled Functions the view knows
// about, sourced from index.Lookup — never every Function in the cluster.
func TestServePoolEndpointsBreakdown(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	view := NewAgentView()
	view.Upsert(headerEntry("ns", "fn", 0))
	ix := endpointcache.NewIndex()
	port := int32(8888)
	ready := true
	ix.ApplySlice(&discoveryv1.EndpointSlice{
		Name:      "fn-slice",
		Namespace: "ns",
		Labels: map[string]string{
			fv1.FUNCTION_NAME:      "fn",
			fv1.FUNCTION_NAMESPACE: "ns",
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
		},
		Ports: []discoveryv1.EndpointPort{{Port: &port}},
	})

	mux := newTestPoolAPI(t, authz, true, false, ix, view)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/registry/pool", nil))
	require.Equal(t, http.StatusOK, w.Code)

	body := decodeJSON[poolResponse](t, w)
	eps, ok := body.Endpoints["ns/fn"]
	require.True(t, ok)
	require.Len(t, eps, 1)
	assert.Equal(t, "10.0.0.1:8888", eps[0].Address)
	assert.True(t, eps[0].Ready)
}

// TestServePoolNamespaceScopeFiltersPodsAndEndpoints pins the auth contract:
// a namespace-scoped token sees only pods/endpoints in its allowed
// namespaces, a wildcard token sees everything.
func TestServePoolNamespaceScopeFiltersPodsAndEndpoints(t *testing.T) {
	t.Parallel()
	key := []byte("pool-test-key")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	view.Upsert(headerEntry("ns-a", "fn-a", 0))
	view.Upsert(headerEntry("ns-b", "fn-b", 0))
	ix := endpointcache.NewIndex()
	port := int32(8888)
	ready := true
	for _, fn := range []struct{ ns, name, ip string }{{"ns-a", "fn-a", "10.0.1.1"}, {"ns-b", "fn-b", "10.0.2.1"}} {
		ix.ApplySlice(&discoveryv1.EndpointSlice{
			Name:      fn.name + "-slice",
			Namespace: fn.ns,
			Labels: map[string]string{
				fv1.FUNCTION_NAME:      fn.name,
				fv1.FUNCTION_NAMESPACE: fn.ns,
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{fn.ip}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
			},
			Ports: []discoveryv1.EndpointPort{{Port: &port}},
		})
	}
	podA := poolTestPod("ns-a", "pod-a", "10.0.1.1", true)
	podB := poolTestPod("ns-b", "pod-b", "10.0.2.1", true)
	mux := newTestPoolAPI(t, authz, true, false, ix, view, podA, podB)

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": []any{"ns-a"},
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/pool", tok))
	require.Equal(t, http.StatusOK, w.Code)

	body := decodeJSON[poolResponse](t, w)
	require.Len(t, body.Pods, 1)
	assert.Equal(t, "pod-a", body.Pods[0].Name)
	_, hasA := body.Endpoints["ns-a/fn-a"]
	_, hasB := body.Endpoints["ns-b/fn-b"]
	assert.True(t, hasA)
	assert.False(t, hasB, "a namespace-scoped token must not see another namespace's endpoints")
}

func TestServePoolWildcardSeesAllNamespaces(t *testing.T) {
	t.Parallel()
	key := []byte("pool-test-key")
	authz := NewAuthorizer(key)
	podA := poolTestPod("ns-a", "pod-a", "10.0.1.1", true)
	podB := poolTestPod("ns-b", "pod-b", "10.0.2.1", true)
	mux := newTestPoolAPI(t, authz, true, false, nil, nil, podA, podB)

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": "*",
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/pool", tok))
	require.Equal(t, http.StatusOK, w.Code)

	body := decodeJSON[poolResponse](t, w)
	assert.Len(t, body.Pods, 2)
}

func TestServePoolNoTokenAuthEnabledUnauthorized(t *testing.T) {
	t.Parallel()
	key := []byte("pool-test-key")
	authz := NewAuthorizer(key)
	mux := newTestPoolAPI(t, authz, true, false, nil, nil)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/registry/pool", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
