// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package publisher

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/utils/loggerfactory"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

func TestPublisher(t *testing.T) {
	fnName := "test-fn"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+fnName, r.URL.Path)
		require.Equal(t, "aaa", r.Header.Get("X-Fission-Test"))
		require.Contains(t, r.Header, "Traceparent")
	}))

	ctx := t.Context()
	logger := loggerfactory.GetLogger()
	shutdown, err := otelUtils.InitProvider(ctx, logger, fnName)
	require.NoError(t, err)
	if shutdown != nil {
		defer shutdown(ctx)
	}

	wp := MakeWebhookPublisher(logger, s.URL)
	wp.Publish(ctx, "", map[string]string{"X-Fission-Test": "aaa"}, http.MethodPost, fnName)
	time.Sleep(time.Second * 1)
}

func TestPublisherSubpath(t *testing.T) {
	subpath := "/api/v1/read"
	fnName := "test-fn-subpath"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/"+fnName+subpath, r.URL.Path)
		require.Equal(t, "aaa", r.Header.Get("X-Fission-Test"))
		require.Contains(t, r.Header, "Traceparent")
	}))

	ctx := t.Context()
	logger := loggerfactory.GetLogger()
	shutdown, err := otelUtils.InitProvider(ctx, logger, fnName)
	require.NoError(t, err)
	if shutdown != nil {
		defer shutdown(ctx)
	}

	wp := MakeWebhookPublisher(logger, s.URL)
	wp.Publish(ctx, "", map[string]string{"X-Fission-Test": "aaa"}, http.MethodGet, fnName+subpath)
	time.Sleep(time.Second * 1)
}

// countingSink is a logr.LogSink that counts error-level log calls; all
// other LogSink behavior is no-op. Used to assert that retried 404s do not
// flood the error log (they log at V(1) until the final give-up).
type countingSink struct {
	mu     sync.Mutex
	errors int
}

func (s *countingSink) Init(logr.RuntimeInfo)    {}
func (s *countingSink) Enabled(int) bool         { return true }
func (s *countingSink) Info(int, string, ...any) {}
func (s *countingSink) Error(error, string, ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors++
}
func (s *countingSink) WithValues(...any) logr.LogSink { return s }
func (s *countingSink) WithName(string) logr.LogSink   { return s }

func (s *countingSink) errorCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errors
}

// TestWebhookPublisherRetriesNotFound verifies that a 404 from the router
// (route not yet reconciled into the mux for a freshly created trigger) is
// treated as transient and retried, instead of dropping the event — and
// that the retried attempts log quietly (V(1)) rather than flooding the
// error log with one error per attempt.
func TestWebhookPublisherRetriesNotFound(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		if hits < 3 {
			http.NotFound(w, r) // route not reconciled yet
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &countingSink{}
	p := MakeWebhookPublisher(logr.New(sink), srv.URL)
	p.Publish(t.Context(), "", map[string]string{}, http.MethodPost, "test-fn")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits >= 3
	}, 15*time.Second, 100*time.Millisecond, "publisher should retry past transient 404s")

	require.Zero(t, sink.errorCount(),
		"retried 404s that eventually succeed must not produce error-level logs")
}

// TestWebhookPublisherDoesNotRetryOtherClientErrors verifies that a 4xx
// other than 404 stays terminal (no retry), matching the pre-existing
// behaviour for bad-request responses.
func TestWebhookPublisherDoesNotRetryOtherClientErrors(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	logger := loggerfactory.GetLogger()
	p := MakeWebhookPublisher(logger, srv.URL)
	p.Publish(t.Context(), "", map[string]string{}, http.MethodPost, "test-fn")

	require.Never(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits > 1
	}, 3*time.Second, 100*time.Millisecond, "publisher should not retry non-404 client errors")
}
