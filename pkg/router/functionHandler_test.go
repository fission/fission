// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
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
	classic.ObjectMeta.UID = "uid-classic"
	stream := fnWithPkg("rv2", "pkg2")
	stream.ObjectMeta.UID = "uid-stream"
	stream.ObjectMeta.Name = "fn-stream"
	stream.Spec.Streaming = &fv1.StreamingConfig{IdleTimeoutSeconds: 7}

	fns := map[string]*fv1.Function{classic.Name: classic, stream.Name: stream}
	timeouts := map[k8stypes.UID]int{classic.GetUID(): 42} // stream falls to default
	const idleDefault = 33 * time.Second

	policies := precomputePolicies(fns, timeouts, idleDefault)
	require.Len(t, policies, 2)

	for _, fn := range fns {
		fnTimeout := timeouts[fn.GetUID()]
		if fnTimeout == 0 {
			fnTimeout = fv1.DEFAULT_FUNCTION_TIMEOUT
		}
		want := resolveProxyPolicy(fn, time.Duration(fnTimeout)*time.Second, idleDefault)
		assert.Equal(t, want, policies[fn.GetUID()], "hoisted policy must match per-request computation for %s", fn.Name)
	}

	// The handler-side lookup helper returns the hoisted entry, and falls back
	// to direct computation for handlers built without the map (test harnesses).
	fh := &functionHandler{
		tsRoundTripperParams: &tsRoundTripperParams{streamIdleDefault: idleDefault},
		policyByUID:          policies,
	}
	assert.Equal(t, policies[stream.GetUID()], fh.proxyPolicyFor(stream, time.Duration(fv1.DEFAULT_FUNCTION_TIMEOUT)*time.Second))
	bare := &functionHandler{tsRoundTripperParams: &tsRoundTripperParams{streamIdleDefault: idleDefault}}
	assert.Equal(t,
		resolveProxyPolicy(classic, 42*time.Second, idleDefault),
		bare.proxyPolicyFor(classic, 42*time.Second),
		"missing map must fall back to direct computation")
}
