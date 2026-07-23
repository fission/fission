// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils/correlation"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestProxyErrorHandler(t *testing.T) {
	logger := loggerfactory.GetLogger()

	fh := &functionHandler{
		logger: logger,
		function: &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dummy",
				Namespace: "dummy-bar",
			},
		},
	}

	errHandler := fh.getProxyErrorHandler(time.Now(), &RetryingRoundTripper{})

	req, err := http.NewRequest("GET", "http://foobar.com", nil)
	require.Nil(t, err)

	req.Header.Set("foo", "bar")
	respRecorder := httptest.NewRecorder()
	errHandler(respRecorder, req, context.Canceled)
	require.Equal(t, 499, respRecorder.Code)

	respRecorder = httptest.NewRecorder()
	errHandler(respRecorder, req, context.DeadlineExceeded)
	require.Equal(t, http.StatusGatewayTimeout, respRecorder.Code)

	respRecorder = httptest.NewRecorder()
	errHandler(respRecorder, req, errors.New("dummy"))
	require.Equal(t, http.StatusInternalServerError, respRecorder.Code)

	// A server-initiated streaming abort surfaces as context.Canceled on `err`,
	// but carries an idle/max sentinel cause on the request context. It must be
	// reported as 504 (with the reason in the body), NOT masqueraded as a 499
	// client-close. Regression guard for the previously-critical bug where the
	// cause was dropped and every stream abort logged as "client closed" at V(1).
	for _, cause := range []error{errStreamIdleTimeout, errStreamMaxDuration} {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(fmt.Errorf("%w (30s)", cause))
		respRecorder = httptest.NewRecorder()
		errHandler(respRecorder, req.WithContext(ctx), context.Canceled)
		require.Equalf(t, http.StatusGatewayTimeout, respRecorder.Code,
			"stream abort (%v) must be 504, not a 499 client-close", cause)
		require.Containsf(t, respRecorder.Body.String(), cause.Error(),
			"504 body should carry the abort reason for %v", cause)
	}
}

func TestClassifyFunctionError(t *testing.T) {
	t.Parallel()
	connRefused := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}

	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{name: "connection refused", err: connRefused, wantReason: ferror.ReasonConnectionRefused},
		{name: "dial error", err: dialErr, wantReason: ferror.ReasonDialError},
		{name: "generic non-network error", err: errors.New("boom"), wantReason: ferror.ReasonFunctionError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantReason, classifyFunctionError(tc.err))
		})
	}
}

// TestProxyErrorHandlerStructuredBody pins the RFC-0015 structured failure body:
// the JSON carries {component, reason, requestId}, the status code is unchanged
// from the legacy mapping, and the X-Fission-Component header attributes the
// failure. Verbose Message is gated behind debug.
func TestProxyErrorHandlerStructuredBody(t *testing.T) {
	logger := loggerfactory.GetLogger()
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "dummy", Namespace: "dummy-bar"}}

	newHandler := func(debug bool) func(http.ResponseWriter, *http.Request, error) {
		fh := &functionHandler{logger: logger, function: fn, structuredErrors: true, isDebugEnv: debug}
		return fh.getProxyErrorHandler(time.Now(), &RetryingRoundTripper{})
	}

	t.Run("executor capacity exceeded keeps 429 and attributes executor", func(t *testing.T) {
		errHandler := newHandler(false)
		invErr := ferror.NewInvocationError(ferror.ComponentExecutor, ferror.ReasonCapacityExceeded,
			ferror.MakeError(ferror.ErrorTooManyRequests, "busy"))

		req := httptest.NewRequest(http.MethodGet, "http://foobar.com", nil)
		req = req.WithContext(correlation.NewContext(req.Context(), "req-123"))
		rec := httptest.NewRecorder()
		errHandler(rec, req, invErr)

		require.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.Equal(t, string(ferror.ComponentExecutor), rec.Header().Get(correlation.HeaderComponent))

		var body ferror.InvocationError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, ferror.ComponentExecutor, body.Component)
		assert.Equal(t, ferror.ReasonCapacityExceeded, body.Reason)
		assert.Equal(t, "req-123", body.RequestID)
		assert.Empty(t, body.Message, "raw detail must not leak without debug")
	})

	t.Run("function dial error attributes function with 500", func(t *testing.T) {
		errHandler := newHandler(false)
		dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}

		req := httptest.NewRequest(http.MethodGet, "http://foobar.com", nil)
		rec := httptest.NewRecorder()
		errHandler(rec, req, dialErr)

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		var body ferror.InvocationError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, ferror.ComponentFunction, body.Component)
		assert.Equal(t, ferror.ReasonDialError, body.Reason)
	})

	t.Run("debug header reveals raw detail only in debug env", func(t *testing.T) {
		errHandler := newHandler(true)
		req := httptest.NewRequest(http.MethodGet, "http://foobar.com", nil)
		req.Header.Set(correlation.HeaderDebug, "true")
		rec := httptest.NewRecorder()
		errHandler(rec, req, errors.New("verbose detail"))

		var body ferror.InvocationError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Contains(t, body.Message, "verbose detail")
	})
}

// recordingExecutor implements eclient.ClientInterface and records the Function
// passed to GetServiceForFunction.
type recordingExecutor struct{ gotFn *fv1.Function }

func (r *recordingExecutor) GetServiceForFunction(_ context.Context, fn *fv1.Function) (string, error) {
	r.gotFn = fn
	return "10.0.0.1:8888", nil
}
func (r *recordingExecutor) TapService(metav1.ObjectMeta, fv1.ExecutorType, url.URL) {}
func (r *recordingExecutor) UnTapService(context.Context, metav1.ObjectMeta, fv1.ExecutorType, *url.URL) error {
	return nil
}

func (r *recordingExecutor) EnsureCapacity(context.Context, *fv1.Function, int, int) (string, error) {
	return "", ferror.MakeError(ferror.ErrorNotFound, "fake executor has no capacity endpoint")
}

func fnWithPkg(rv, pkg string) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", ResourceVersion: rv}}
	fn.Spec.Package.PackageRef.Name = pkg
	return fn
}

// TestResolverFromExecutorReReadsCurrentFunction guards the TestGoEnv fix:
// the executor must be specialized with the current Function (re-read from the
// Manager cache), not the resolver's stale snapshot — otherwise a poolmgr function
// keeps serving the old package after `fn update --pkg`.
func TestResolverFromExecutorReReadsCurrentFunction(t *testing.T) {
	logger := loggerfactory.GetLogger()
	stale := fnWithPkg("1", "pkg-v1") // the resolver snapshot the handler captured
	fresh := fnWithPkg("2", "pkg-v2") // what the Manager cache now holds

	reader := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(fresh).Build()
	exec := &recordingExecutor{}
	r := &executorResolver{logger: logger, reader: reader, executor: exec}

	_, err := r.fromExecutor(t.Context(), stale)
	require.NoError(t, err)
	require.NotNil(t, exec.gotFn)
	assert.Equal(t, "pkg-v2", exec.gotFn.Spec.Package.PackageRef.Name, "executor must get the re-read function, not the stale snapshot")
	assert.Equal(t, "2", exec.gotFn.ResourceVersion)
}

// TestResolverFromExecutorFallsBackToSnapshot: with no reader the captured
// snapshot is used.
func TestResolverFromExecutorFallsBackToSnapshot(t *testing.T) {
	logger := loggerfactory.GetLogger()
	snap := fnWithPkg("1", "pkg-v1")
	exec := &recordingExecutor{}
	r := &executorResolver{logger: logger, executor: exec} // reader nil

	_, err := r.fromExecutor(t.Context(), snap)
	require.NoError(t, err)
	require.NotNil(t, exec.gotFn)
	assert.Equal(t, "pkg-v1", exec.gotFn.Spec.Package.PackageRef.Name)
}

// TestPrecomputedPolicyParity pins the RFC-0014 hoist: the policy looked up
// from the build-time map must equal what resolveProxyPolicy computes per
// request — including the canary case where the backend (and so the policy)
// is selected per request by UID.
func TestPrecomputedPolicyParity(t *testing.T) {
	t.Parallel()
	classic := fnWithPkg("rv1", "pkg1")
	classic.UID = "uid-classic"
	stream := fnWithPkg("rv2", "pkg2")
	stream.UID = "uid-stream"
	stream.Name = "fn-stream"
	stream.Spec.Streaming = &fv1.StreamingConfig{IdleTimeoutSeconds: 7}

	fns := map[string]*fv1.Function{classic.Name: classic, stream.Name: stream}
	timeouts := map[crd.CacheKeyUG]int{crd.CacheKeyUGFromMeta(&classic.ObjectMeta): 42} // stream falls to default
	const idleDefault = 33 * time.Second

	policies := precomputePolicies(fns, timeouts, idleDefault)
	require.Len(t, policies, 2)

	for _, fn := range fns {
		key := crd.CacheKeyUGFromMeta(&fn.ObjectMeta)
		fnTimeout := timeouts[key]
		if fnTimeout == 0 {
			fnTimeout = fv1.DEFAULT_FUNCTION_TIMEOUT
		}
		want := resolveProxyPolicy(fn, time.Duration(fnTimeout)*time.Second, idleDefault)
		assert.Equal(t, want, policies[key], "hoisted policy must match per-request computation for %s", fn.Name)
	}

	// The handler-side lookup helper returns the hoisted entry, and falls back
	// to direct computation for handlers built without the map (test harnesses).
	fh := &functionHandler{
		tsRoundTripperParams: &tsRoundTripperParams{streamIdleDefault: idleDefault},
		policyByUID:          policies,
	}
	assert.Equal(t, policies[crd.CacheKeyUGFromMeta(&stream.ObjectMeta)], fh.proxyPolicyFor(stream, time.Duration(fv1.DEFAULT_FUNCTION_TIMEOUT)*time.Second))
	bare := &functionHandler{tsRoundTripperParams: &tsRoundTripperParams{streamIdleDefault: idleDefault}}
	assert.Equal(t,
		resolveProxyPolicy(classic, 42*time.Second, idleDefault),
		bare.proxyPolicyFor(classic, 42*time.Second),
		"missing map must fall back to direct computation")
}

// TestPerVersionTimeoutAndPolicyDoNotCollideOnSharedUID is the RFC-0025
// plan-review regression (warning #5): two resolved *fv1.Function snapshots
// of the same versioned function -- e.g. a weighted alias's primary and
// secondary target -- share a UID (versioning.VersionedFunction always
// copies live's identity) but differ in Generation (each pins a different
// FunctionVersion). Keying functionTimeoutMap/policyByUID on UID alone would
// collapse the two into one entry and silently serve one snapshot's
// timeout/streaming policy to the other; keying on crd.CacheKeyUG (UID,
// Generation) keeps them distinct.
func TestPerVersionTimeoutAndPolicyDoNotCollideOnSharedUID(t *testing.T) {
	sharedUID := k8stypes.UID("shared-fn-uid")

	primary := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: sharedUID, Generation: 1},
		Spec:       fv1.FunctionSpec{FunctionTimeout: 10},
	}
	secondary := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: sharedUID, Generation: 2},
		Spec: fv1.FunctionSpec{
			FunctionTimeout: 99,
			Streaming:       &fv1.StreamingConfig{IdleTimeoutSeconds: 3},
		},
	}
	require.Equal(t, primary.UID, secondary.UID, "precondition: both snapshots share one UID")
	require.NotEqual(t, primary.Generation, secondary.Generation, "precondition: Generations differ")

	primaryKey := crd.CacheKeyUGFromMeta(&primary.ObjectMeta)
	secondaryKey := crd.CacheKeyUGFromMeta(&secondary.ObjectMeta)
	require.NotEqual(t, primaryKey, secondaryKey, "CacheKeyUG must differ even though the UID is shared")

	fnTimeoutMap := map[crd.CacheKeyUG]int{primaryKey: 10, secondaryKey: 99}
	const idleDefault = 20 * time.Second
	policies := precomputePolicies(
		map[string]*fv1.Function{"primary": primary, "secondary": secondary},
		fnTimeoutMap, idleDefault)
	require.Len(t, policies, 2, "both snapshots must get their own policy entry")

	fh := &functionHandler{
		tsRoundTripperParams: &tsRoundTripperParams{streamIdleDefault: idleDefault},
		functionTimeoutMap:   fnTimeoutMap,
		policyByUID:          policies,
	}

	assert.Equal(t, 10, fh.functionTimeoutMap[primaryKey])
	assert.Equal(t, 99, fh.functionTimeoutMap[secondaryKey])

	primaryPolicy := fh.proxyPolicyFor(primary, 10*time.Second)
	secondaryPolicy := fh.proxyPolicyFor(secondary, 99*time.Second)
	assert.Equal(t, 10*time.Second, primaryPolicy.maxDuration, "primary's own timeout, not collapsed with secondary's")
	assert.False(t, primaryPolicy.streaming, "primary snapshot has no Streaming config")
	assert.True(t, secondaryPolicy.streaming, "secondary snapshot's Streaming config must not be shadowed by primary's")
	assert.Equal(t, 3*time.Second, secondaryPolicy.idleTimeout, "secondary's own idle timeout, not collapsed with primary's (no streaming)")
}
