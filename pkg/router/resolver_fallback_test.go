// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// fnSlice builds a Fission-labeled EndpointSlice for fn with ready endpoints.
func fnSlice(name, fnName, fnNamespace string, addrs ...string) *discoveryv1.EndpointSlice {
	port := int32(8888)
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "fn-ns",
			Labels: map[string]string{
				fv1.FUNCTION_NAME:      fnName,
				fv1.FUNCTION_NAMESPACE: fnNamespace,
				fv1.MANAGED_BY_LABEL:   fv1.MANAGED_BY_VALUE,
			},
		},
		Ports: []discoveryv1.EndpointPort{{Port: &port}},
	}
	ready := true
	for _, a := range addrs {
		es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{a},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef:  &apiv1.ObjectReference{Kind: "Pod", UID: types.UID("pod-" + a)},
		})
	}
	return es
}

// stubExecutor answers GetServiceForFunction with a fixed address and counts calls.
type stubExecutor struct {
	addr  string
	calls int
}

func (s *stubExecutor) GetServiceForFunction(_ context.Context, _ *fv1.Function) (string, error) {
	s.calls++
	return s.addr, nil
}
func (s *stubExecutor) TapService(metav1.ObjectMeta, fv1.ExecutorType, url.URL) {}
func (s *stubExecutor) UnTapService(context.Context, metav1.ObjectMeta, fv1.ExecutorType, *url.URL) error {
	return nil
}
func (s *stubExecutor) EnsureCapacity(context.Context, *fv1.Function, int, int) (string, error) {
	return "", ferror.MakeError(ferror.ErrorNotFound, "stub executor has no capacity endpoint")
}

// stubCapacity answers EnsureCapacity.
type stubCapacity struct {
	addr  string
	err   error
	calls int
}

func (s *stubCapacity) EnsureCapacity(context.Context, *fv1.Function, int, int) (string, error) {
	s.calls++
	return s.addr, s.err
}

func newFallbackForTest(t *testing.T, ix *endpointcache.Index, exec *stubExecutor, capacity CapacityClient) *fallbackResolver {
	t.Helper()
	logger := loggerfactory.GetLogger()
	er := &executorResolver{
		logger:    logger,
		fmap:      makeFunctionServiceMap(logger, time.Minute),
		executor:  exec,
		throttler: throttler.MakeThrottler(30 * time.Second),
	}
	return newFallbackResolver(logger, ix, er, capacity, false)
}

func poolFn(name string) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "u1"}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypePoolmgr
	return fn
}

func TestFallbackPoolmgrWarmHitAdmitsFromIndex(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-a", "default", "10.0.0.1"))
	exec := &stubExecutor{addr: "10.9.9.9:8888"}
	f := newFallbackForTest(t, ix, exec, nil)

	entry, err := f.Resolve(t.Context(), poolFn("fn-a"), "")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:8888", entry.SvcURL.Host)
	assert.True(t, entry.FromCache)
	require.NotNil(t, entry.Release, "router-local accounting must hand back a release")
	assert.Zero(t, exec.calls, "warm hit must not RPC the executor")
	entry.Release()
}

func TestFallbackPoolmgrColdStartUsesExecutor(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	exec := &stubExecutor{addr: "10.9.9.9:8888"}
	f := newFallbackForTest(t, ix, exec, nil)

	entry, err := f.Resolve(t.Context(), poolFn("fn-cold"), "")
	require.NoError(t, err)
	assert.Equal(t, "10.9.9.9:8888", entry.SvcURL.Host)
	assert.Nil(t, entry.Release, "executor-side accounting — no local release")
	assert.Equal(t, 1, exec.calls)
}

func TestFallbackStrictAnnotationBypassesIndex(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-strict", "default", "10.0.0.1"))
	exec := &stubExecutor{addr: "10.9.9.9:8888"}
	f := newFallbackForTest(t, ix, exec, nil)

	fn := poolFn("fn-strict")
	fn.Annotations = map[string]string{fv1.ConcurrencyEnforcementAnnotation: fv1.ConcurrencyEnforcementStrict}
	entry, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.Equal(t, "10.9.9.9:8888", entry.SvcURL.Host, "strict mode must take the legacy RPC path")
	assert.Equal(t, 1, exec.calls)
}

func TestFallbackSaturatedUsesEnsureCapacity(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-busy", "default", "10.0.0.1"))
	exec := &stubExecutor{addr: "10.9.9.9:8888"}
	capacity := &stubCapacity{addr: "10.0.0.2:8888"}
	f := newFallbackForTest(t, ix, exec, capacity)

	fn := poolFn("fn-busy") // requestsPerPod defaults to 1

	// First request takes the only slot.
	first, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	require.NotNil(t, first.Release)

	// Second request finds the endpoint saturated → ensureCapacity.
	second, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.2:8888", second.SvcURL.Host)
	assert.Nil(t, second.Release, "capacity pod is executor-accounted")
	assert.Equal(t, 1, capacity.calls)
	assert.Zero(t, exec.calls)

	// Releasing the first slot makes the endpoint admissible again.
	first.Release()
	third, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:8888", third.SvcURL.Host)
	third.Release()
}

func TestFallbackSaturatedDegradesWhenCapacityUnsupported(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-busy2", "default", "10.0.0.1"))
	exec := &stubExecutor{addr: "10.9.9.9:8888"}
	capacity := &stubCapacity{err: ferror.MakeError(ferror.ErrorNotFound, "no such endpoint")}
	f := newFallbackForTest(t, ix, exec, capacity)

	fn := poolFn("fn-busy2")
	first, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	defer first.Release()

	// Saturated + 404 from ensureCapacity → legacy RPC (old executor).
	second, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.Equal(t, "10.9.9.9:8888", second.SvcURL.Host)
	assert.Equal(t, 1, exec.calls)
}

func TestFallbackSaturatedRelays429(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-capped", "default", "10.0.0.1"))
	capacity := &stubCapacity{err: ferror.MakeError(ferror.ErrorTooManyRequests, "concurrency limit reached")}
	f := newFallbackForTest(t, ix, &stubExecutor{}, capacity)

	fn := poolFn("fn-capped")
	first, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	defer first.Release()

	_, err = f.Resolve(t.Context(), fn, "")
	require.Error(t, err)
	code, _ := ferror.GetHTTPError(err)
	assert.Equal(t, http.StatusTooManyRequests, code)
}

func TestFallbackNewdeployScaledUpUsesCachePath(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-nd", "default", "10.0.0.1"))
	exec := &stubExecutor{addr: "svc-fn-nd.default"}
	f := newFallbackForTest(t, ix, exec, nil)

	fn := poolFn("fn-nd")
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeNewdeploy

	entry, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.Equal(t, "svc-fn-nd.default", entry.SvcURL.Host)
	assert.Equal(t, 1, exec.calls, "first call populates the address cache")

	entry2, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.True(t, entry2.FromCache)
	assert.Equal(t, 1, exec.calls, "second call is a cache hit")
}

func TestFallbackNewdeployScaleFromZeroBypassesCache(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex() // no slices: scaled to zero
	exec := &stubExecutor{addr: "svc-fn-zero.default"}
	f := newFallbackForTest(t, ix, exec, nil)

	fn := poolFn("fn-zero")
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeNewdeploy

	// Two sequential calls: both must RPC (the cached DNS would dial into a
	// backendless Service), proving the cache bypass while endpoints are zero.
	for want := 1; want <= 2; want++ {
		entry, err := f.Resolve(t.Context(), fn, "")
		require.NoError(t, err)
		assert.Equal(t, "svc-fn-zero.default", entry.SvcURL.Host)
		assert.Equal(t, want, exec.calls)
	}
}

func TestFallbackInvalidateQuarantinesEndpoint(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-q", "default", "10.0.0.1"))
	exec := &stubExecutor{addr: "10.9.9.9:8888"}
	f := newFallbackForTest(t, ix, exec, nil)

	fn := poolFn("fn-q")
	addr := mustParseURL(t, "http://10.0.0.1:8888")
	f.Invalidate(fn, addr, InvalidateHard)

	// The quarantined endpoint is unadmissible. With one ready-but-quarantined
	// endpoint this takes the saturated-fallback branch (ready > 0, nil
	// capacity client → legacy RPC), not the ready==0 cold-start branch.
	entry, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.Equal(t, "10.9.9.9:8888", entry.SvcURL.Host)

	// A slice event lifts the quarantine.
	ix.ApplySlice(fnSlice("s1", "fn-q", "default", "10.0.0.1"))
	entry2, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:8888", entry2.SvcURL.Host)
	entry2.Release()
}

// TestFallbackOnceOnlyBypassesIndex: OnceOnly pods serve exactly one request —
// even when (stale) slices list them, the resolver must take the executor path.
func TestFallbackOnceOnlyBypassesIndex(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-once", "default", "10.0.0.1"))
	exec := &stubExecutor{addr: "10.9.9.9:8888"}
	f := newFallbackForTest(t, ix, exec, nil)

	fn := poolFn("fn-once")
	fn.Spec.OnceOnly = true
	entry, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	assert.Equal(t, "10.9.9.9:8888", entry.SvcURL.Host, "OnceOnly must never be admitted from slices")
	assert.Equal(t, 1, exec.calls)
}

// newEndpointLBForTest builds the fallback resolver with endpoint LB on.
func newEndpointLBForTest(t *testing.T, ix *endpointcache.Index, exec *stubExecutor) *fallbackResolver {
	t.Helper()
	logger := loggerfactory.GetLogger()
	er := &executorResolver{
		logger:    logger,
		fmap:      makeFunctionServiceMap(logger, time.Minute),
		executor:  exec,
		throttler: throttler.MakeThrottler(30 * time.Second),
	}
	return newFallbackResolver(logger, ix, er, nil, true)
}

func newdeployFn(name string) *fv1.Function {
	fn := poolFn(name)
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeNewdeploy
	return fn
}

func TestFallbackEndpointLBDialsPodsDirectly(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-lb", "default", "10.0.0.1", "10.0.0.2"))
	exec := &stubExecutor{addr: "svc-fn-lb.default"}
	f := newEndpointLBForTest(t, ix, exec)

	fn := newdeployFn("fn-lb")

	// Two held admissions spread across the two pods (least outstanding),
	// while taps stay keyed on the Service address the executor knows.
	first, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	require.NotNil(t, first.Release, "endpoint-LB entries carry router-local accounting")
	require.NotNil(t, first.TapURL)
	assert.Equal(t, "svc-fn-lb.default", first.TapURL.Host, "taps must target the Service, not the pod")

	second, err := f.Resolve(t.Context(), fn, "")
	require.NoError(t, err)
	require.NotNil(t, second.Release)
	assert.NotEqual(t, first.SvcURL.Host, second.SvcURL.Host,
		"least-outstanding selection must spread held requests across pods")
	for _, host := range []string{first.SvcURL.Host, second.SvcURL.Host} {
		assert.Contains(t, []string{"10.0.0.1:8888", "10.0.0.2:8888"}, host)
	}
	first.Release()
	second.Release()
}

func TestFallbackEndpointLBFallsBackToVIPWhenQuarantined(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-lbq", "default", "10.0.0.1"))
	ix.Quarantine("default", "fn-lbq", "10.0.0.1:8888")
	exec := &stubExecutor{addr: "svc-fn-lbq.default"}
	f := newEndpointLBForTest(t, ix, exec)

	entry, err := f.Resolve(t.Context(), newdeployFn("fn-lbq"), "")
	require.NoError(t, err)
	assert.Equal(t, "svc-fn-lbq.default", entry.SvcURL.Host, "quarantined endpoints degrade to the Service VIP")
	assert.Nil(t, entry.Release)
}

// TestFallbackStickyKeyReachesAdmit pins the RFC-0023 threading: the sticky
// key handed to Resolve drives the index pick deterministically (same key,
// same endpoint across repeated resolves and load skew), while the empty key
// keeps least-outstanding, and the Release accounting seam is unchanged.
func TestFallbackStickyKeyReachesAdmit(t *testing.T) {
	t.Parallel()
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-a", "default", "10.0.0.1", "10.0.0.2", "10.0.0.3"))
	f := newFallbackForTest(t, ix, &stubExecutor{addr: "10.9.9.9:8888"}, nil)

	fn := poolFn("fn-a")
	fn.Spec.RequestsPerPod = 10

	first, err := f.Resolve(t.Context(), fn, "session-42")
	require.NoError(t, err)
	require.NotNil(t, first.Release)
	for range 5 {
		again, err := f.Resolve(t.Context(), fn, "session-42")
		require.NoError(t, err)
		assert.Equal(t, first.SvcURL.Host, again.SvcURL.Host, "same key must keep landing on its owner")
		again.Release()
	}
	first.Release()
}
