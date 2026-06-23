// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/correlation"
)

// errorRecordingSink counts Error-level log calls so a test can assert that
// routine churn does NOT escalate to Error.
type errorRecordingSink struct {
	mu        sync.Mutex
	errCalls  int
	lastErrMs string
}

func (s *errorRecordingSink) Init(logr.RuntimeInfo)          {}
func (s *errorRecordingSink) Enabled(int) bool               { return true }
func (s *errorRecordingSink) Info(int, string, ...any)       {}
func (s *errorRecordingSink) WithValues(...any) logr.LogSink { return s }
func (s *errorRecordingSink) WithName(string) logr.LogSink   { return s }
func (s *errorRecordingSink) Error(_ error, msg string, _ ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errCalls++
	s.lastErrMs = msg
}
func (s *errorRecordingSink) errors() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errCalls
}

// newTestClient builds a client pointed at url with a plain http.Client (no
// retry transport) so 5xx responses don't add backoff delay to the test.
func newTestClient(sink logr.LogSink, url string) *client {
	return &client{
		logger:      logr.New(sink),
		executorURL: url,
		tappedByURL: make(map[string]TapServiceRequest),
		requestChan: make(chan TapServiceRequest, 100),
		httpClient:  &http.Client{},
	}
}

// TestRequestIDPropagation locks RFC-0015: the executor RPCs carry the
// per-invocation X-Fission-Request-ID from the context so the executor can
// correlate a cold-start with the router request that triggered it.
func TestRequestIDPropagation(t *testing.T) {
	t.Parallel()
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"}}

	tests := []struct {
		name string
		call func(c *client, ctx context.Context) error
	}{
		{
			name: "GetServiceForFunction",
			call: func(c *client, ctx context.Context) error {
				_, err := c.GetServiceForFunction(ctx, fn)
				return err
			},
		},
		{
			name: "EnsureCapacity",
			call: func(c *client, ctx context.Context) error {
				_, err := c.EnsureCapacity(ctx, fn, 0, 0)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotID string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotID = r.Header.Get(correlation.HeaderRequestID)
				_, _ = w.Write([]byte("10.0.0.1:8888"))
			}))
			defer srv.Close()

			c := newTestClient(&errorRecordingSink{}, srv.URL)
			ctx := correlation.NewContext(t.Context(), "req-xyz")
			require.NoError(t, tc.call(c, ctx))
			assert.Equal(t, "req-xyz", gotID, "executor RPC must carry the request id")
		})
	}
}

// TestFlushTapsNotFoundDoesNotEscalate locks the fix: a 404 from the executor
// (some tapped addresses expired — routine churn) must reset the failure
// counter and never reach the Error-level "serving pods at risk" escalation,
// even when it happens many times in a row. Only genuine failures escalate.
func TestFlushTapsNotFoundDoesNotEscalate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Not found", http.StatusNotFound)
	}))
	defer srv.Close()

	sink := &errorRecordingSink{}
	c := newTestClient(sink, srv.URL)
	batch := map[string]TapServiceRequest{"u": {ServiceURL: "http://10.0.0.1:8888"}}

	before := counterValue(t, "fission_router_tap_flush_notfound_total")
	rounds := tapFailureEscalation + 3
	for range rounds {
		c.flushTaps(batch)
	}
	assert.Zero(t, sink.errors(), "routine 404 churn must not escalate to Error")
	assert.Zero(t, c.tapFailures.Load(), "a 404 must reset the failure counter")
	// The 404s stay observable via a distinct counter, so a SUSTAINED rate
	// (an index/registration drift, not churn) is still visible without
	// tripping the failure alarm.
	assert.Equal(t, float64(rounds), counterValue(t, "fission_router_tap_flush_notfound_total")-before,
		"each 404 flush must increment the NotFound counter")
}

// TestFlushTapsGenuineFailureEscalates is the other half: a real failure
// (5xx — executor errored) must escalate to Error once it persists past the
// threshold, so a truly stuck tap path is still surfaced.
func TestFlushTapsGenuineFailureEscalates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink := &errorRecordingSink{}
	c := newTestClient(sink, srv.URL)
	batch := map[string]TapServiceRequest{"u": {ServiceURL: "http://10.0.0.1:8888"}}

	for range tapFailureEscalation {
		c.flushTaps(batch)
	}
	require.Positive(t, sink.errors(), "a persistent genuine failure must escalate to Error")
	assert.Equal(t, "tap flush failing persistently; idle reaper may reap serving pods", sink.lastErrMs)
}
