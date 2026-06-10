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
	return newFallbackResolver(logger, ix, er, capacity)
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

	entry, err := f.Resolve(t.Context(), poolFn("fn-a"))
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

	entry, err := f.Resolve(t.Context(), poolFn("fn-cold"))
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
	fn.Annotations = map[string]string{ConcurrencyEnforcementAnnotation: "strict"}
	entry, err := f.Resolve(t.Context(), fn)
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
	first, err := f.Resolve(t.Context(), fn)
	require.NoError(t, err)
	require.NotNil(t, first.Release)

	// Second request finds the endpoint saturated → ensureCapacity.
	second, err := f.Resolve(t.Context(), fn)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.2:8888", second.SvcURL.Host)
	assert.Nil(t, second.Release, "capacity pod is executor-accounted")
	assert.Equal(t, 1, capacity.calls)
	assert.Zero(t, exec.calls)

	// Releasing the first slot makes the endpoint admissible again.
	first.Release()
	third, err := f.Resolve(t.Context(), fn)
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
	first, err := f.Resolve(t.Context(), fn)
	require.NoError(t, err)
	defer first.Release()

	// Saturated + 404 from ensureCapacity → legacy RPC (old executor).
	second, err := f.Resolve(t.Context(), fn)
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
	first, err := f.Resolve(t.Context(), fn)
	require.NoError(t, err)
	defer first.Release()

	_, err = f.Resolve(t.Context(), fn)
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

	entry, err := f.Resolve(t.Context(), fn)
	require.NoError(t, err)
	assert.Equal(t, "svc-fn-nd.default", entry.SvcURL.Host)
	assert.Equal(t, 1, exec.calls, "first call populates the address cache")

	entry2, err := f.Resolve(t.Context(), fn)
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
		entry, err := f.Resolve(t.Context(), fn)
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
	f.Invalidate(fn, addr)

	// The quarantined endpoint is unadmissible → cold-start RPC path.
	entry, err := f.Resolve(t.Context(), fn)
	require.NoError(t, err)
	assert.Equal(t, "10.9.9.9:8888", entry.SvcURL.Host)

	// A slice event lifts the quarantine.
	ix.ApplySlice(fnSlice("s1", "fn-q", "default", "10.0.0.1"))
	entry2, err := f.Resolve(t.Context(), fn)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:8888", entry2.SvcURL.Host)
	entry2.Release()
}
