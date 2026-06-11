// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// newTestClient builds a client pointed at url with retries disabled (so 5xx
// responses don't add backoff delay to the test).
func newTestClient(sink logr.LogSink, url string) *client {
	hc := retryablehttp.NewClient()
	hc.RetryMax = 0
	hc.Logger = nil
	return &client{
		logger:      logr.New(sink),
		executorURL: url,
		tappedByURL: make(map[string]TapServiceRequest),
		requestChan: make(chan TapServiceRequest, 100),
		httpClient:  hc,
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

	for range tapFailureEscalation + 3 {
		c.flushTaps(batch)
	}
	assert.Zero(t, sink.errors(), "routine 404 churn must not escalate to Error")
	assert.Zero(t, c.tapFailures.Load(), "a 404 must reset the failure counter")
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
