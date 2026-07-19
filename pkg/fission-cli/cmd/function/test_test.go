// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/router/asyncinvoke"
)

func TestRenderInvocationFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		component string
		status    int
		body      string
		contains  []string
		absent    []string
	}{
		{
			name:      "structured executor failure names component, reason, status, and the body's request id",
			component: "executor",
			status:    503,
			body:      `{"component":"executor","reason":"specialization_failed","requestId":"req-abc"}`,
			contains:  []string{"executor", "specialization_failed", "503", "req-abc"},
		},
		{
			name:      "debug message is surfaced when present",
			component: "function",
			status:    500,
			body:      `{"component":"function","reason":"function_error","message":"panic: boom"}`,
			contains:  []string{"function", "function_error", "detail: panic: boom"},
		},
		{
			name:      "structured failure without a request id still renders",
			component: "timeout",
			status:    504,
			body:      `{"component":"timeout","reason":"function_timeout"}`,
			contains:  []string{"timeout", "function_timeout", "504"},
			absent:    []string{"request "},
		},
		{
			name:     "legacy plain-text body (no component header) falls back to the raw body",
			status:   502,
			body:     "upstream connect error",
			contains: []string{"returned 502", "upstream connect error"},
			absent:   []string{"failed in"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			renderInvocationFailure(&buf, "hello", tc.status, tc.component, []byte(tc.body))
			out := buf.String()
			for _, c := range tc.contains {
				assert.Containsf(t, out, c, "output:\n%s", out)
			}
			for _, a := range tc.absent {
				assert.NotContainsf(t, out, a, "output:\n%s", out)
			}
		})
	}
}

// TestDoSyncUnauthorized guards the sync `fission fn test` path's 401/403
// handling (issue #3588): a router rejection must surface a clear
// FISSION_INTERNAL_AUTH_SECRET hint instead of falling through to the
// generic failure renderer + pod-log lookup, which produces the original
// bug's misleading "no active pods found" output.
//
// Not run in parallel: cmd.SetClientset installs a package-level client
// shared by every test in this package.
func TestDoSyncUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"},
	}
	fc := fissionfake.NewSimpleClientset(fn) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "fn")
	in.Set(flagkey.HtMethod, []string{http.MethodGet})

	err := (&TestSubCommand{}).do(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FISSION_INTERNAL_AUTH_SECRET")
}

// TestDoAsyncDispatch guards the --async wiring end-to-end: do() must send
// the async invoke-mode header and hand the response to handleAsyncResponse
// rather than falling through to the sync status-handling code (which would
// silently print the raw 202 body instead of the invocation id — this is
// exactly what broke, unnoticed by unit tests, partway through refactoring
// do() to share combinedHTTPRequest with the sync path).
//
// Not run in parallel: cmd.SetClientset installs a package-level client
// shared by every test in this package.
func TestDoAsyncDispatch(t *testing.T) {
	var gotInvokeMode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInvokeMode = r.Header.Get(asyncinvoke.HeaderInvokeMode)
		w.Header().Set(asyncinvoke.HeaderInvocationID, "asyncinv/42")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"},
	}
	fc := fissionfake.NewSimpleClientset(fn) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "fn")
	in.Set(flagkey.HtMethod, []string{http.MethodGet})
	in.Set(flagkey.FnTestAsync, true)

	err := (&TestSubCommand{}).do(in)
	require.NoError(t, err)
	assert.Equal(t, asyncinvoke.InvokeModeAsync, gotInvokeMode, "do() must set the async invoke-mode header")
}

// TestDoSyncGenericFailure guards the non-401/403 failure branch: a router
// error must render the RFC-0015 failure attribution, attempt the pod-log
// fallback (and its own log-database fallback, both errors here since the
// fake clientsets have no matching pods and no log db configured), and
// return the generic failure — not the 401/403 FISSION_INTERNAL_AUTH_SECRET
// hint, which is unique to TestDoSyncUnauthorized.
//
// Not run in parallel: cmd.SetClientset installs a package-level client
// shared by every test in this package.
func TestDoSyncGenericFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"},
	}
	fc := fissionfake.NewClientset(fn)
	kc := k8sfake.NewClientset()
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, KubernetesClient: kc, Namespace: "default"})

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "fn")
	in.Set(flagkey.HtMethod, []string{http.MethodGet})

	err := (&TestSubCommand{}).do(in)
	require.Error(t, err)
	assert.Equal(t, "error getting function response", err.Error())
	assert.NotContains(t, err.Error(), "FISSION_INTERNAL_AUTH_SECRET")
}

// TestDoSubPathHandling guards both branches of the --subpath handling: a
// leading slash on the user-supplied subpath must not be doubled, and a
// missing one must be inserted — both must land on the identical final path.
//
// Not run in parallel: cmd.SetClientset installs a package-level client
// shared by every test in this package.
func TestDoSubPathHandling(t *testing.T) {
	cases := []struct {
		name    string
		subPath string
	}{
		{"without leading slash", "extra/path"},
		{"with leading slash", "/extra/path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

			fn := &fv1.Function{
				ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"},
			}
			fc := fissionfake.NewClientset(fn)
			cmd.ResetClientsetForTest()
			cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

			in := dummy.TestFlagSet()
			in.Set(flagkey.FnName, "fn")
			in.Set(flagkey.HtMethod, []string{http.MethodGet})
			in.Set(flagkey.FnSubPath, tc.subPath)

			err := (&TestSubCommand{}).do(in)
			require.NoError(t, err)
			assert.Equal(t, "/fission-function/fn/extra/path", gotPath)
		})
	}
}

// TestDoSyncSmallerTestTimeoutGoverns guards the --timeout selection branch:
// when --timeout is set and smaller than the function's own FunctionTimeout,
// the smaller one must bound the request context. The function's spec
// timeout is set to 60s and --timeout to 1ns — if do() picked the 60s spec
// timeout instead, this fast local httptest server would answer well within
// it and the call would succeed; a 1ns budget instead expires before the
// request can complete, which is what proves the smaller value governs. This
// uses a deliberately tiny (not zero) deadline rather than a real sleep, so
// the test is instant and can't flake under CI load — see
// .claude/resources/test-writing-guidelines.md on avoiding sleeps for
// time-dependent behavior.
//
// Not run in parallel: cmd.SetClientset installs a package-level client
// shared by every test in this package.
func TestDoSyncSmallerTestTimeoutGoverns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		Spec:       fv1.FunctionSpec{FunctionTimeout: 60},
	}
	fc := fissionfake.NewClientset(fn)
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "fn")
	in.Set(flagkey.HtMethod, []string{http.MethodGet})
	in.Set(flagkey.FnTestTimeout, 1*time.Nanosecond)

	err := (&TestSubCommand{}).do(in)
	require.Error(t, err, "a 1ns --timeout must govern over the 60s function spec timeout")
	// net/http surfaces context.WithTimeoutCause's custom message (Go 1.21+),
	// so the error names the 1ns budget that was actually applied rather than
	// the 60s function spec timeout.
	assert.Contains(t, err.Error(), "function request timeout (1ns) exceeded")
}
