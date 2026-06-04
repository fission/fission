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

// TestWebhookPublisherRetriesNotFound verifies that a 404 from the router
// (route not yet reconciled into the mux for a freshly created trigger) is
// treated as transient and retried, instead of dropping the event.
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

	logger := loggerfactory.GetLogger()
	p := MakeWebhookPublisher(logger, srv.URL)
	p.Publish(t.Context(), "", map[string]string{}, http.MethodPost, "test-fn")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits >= 3
	}, 15*time.Second, 100*time.Millisecond, "publisher should retry past transient 404s")
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
