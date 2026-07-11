// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestServe(t *testing.T) {
	mgr := &errgroup.Group{}
	t.Cleanup(func() { _ = mgr.Wait() })

	ctx := t.Context()
	logger := loggerfactory.GetLogger()
	// A free port (not a hardcoded one) avoids a bind conflict with anything
	// else on the runner. httpmux's Handle("/") is exact, so /notfound still 404s.
	addr := freePort(t)
	m := httpmux.New()
	m.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("test handler"))
		if err != nil {
			logger.Error(err, "failed to write response")
		}
	}))

	mgr.Go(func() error {
		Serve(ctx, logger, mgr, ServerOptions{Name: "test", Addr: addr, Handler: m.Handler()})
		return nil
	})

	// Wait for the server to start accepting connections before requesting.
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 3*time.Second, 20*time.Millisecond)

	tests := []struct {
		Name       string
		URL        string
		StatusCode int
		Body       string
	}{
		{
			Name:       "test handler",
			URL:        "http://" + addr,
			StatusCode: http.StatusOK,
			Body:       "test handler",
		},
		{
			Name:       "not found",
			URL:        "http://" + addr + "/notfound",
			StatusCode: http.StatusNotFound,
			Body:       "404 page not found\n",
		},
	}
	client := &http.Client{}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			resp, err := client.Get(test.URL) //nolint:noctx
			require.NoError(t, err, "failed to make get request %s", test.URL)
			defer resp.Body.Close()
			require.Equal(t, test.StatusCode, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, string(body), test.Body)
		})
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// TestServeDrainsInFlightRequest verifies that an in-flight request is
// allowed to complete when the server is asked to shut down, rather than being
// cut the moment the signal context is cancelled.
func TestServeDrainsInFlightRequest(t *testing.T) {
	addr := freePort(t)

	handlerEntered := make(chan struct{})
	m := http.NewServeMux()
	m.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(handlerEntered)
		// Simulate work that outlives the shutdown signal.
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	mgr := &errgroup.Group{}
	go Serve(ctx, logr.Discard(), mgr, ServerOptions{Name: "test", Addr: addr, Handler: m})

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 3*time.Second, 20*time.Millisecond)

	type result struct {
		status int
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow") //nolint:noctx
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		resCh <- result{status: resp.StatusCode}
	}()

	// Once the handler is executing, trigger shutdown mid-request.
	<-handlerEntered
	cancel()

	select {
	case res := <-resCh:
		require.NoError(t, res.err, "in-flight request should drain, not be cut")
		assert.Equal(t, http.StatusOK, res.status)
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not complete during graceful drain")
	}

	werr := make(chan error, 1)
	go func() { werr <- mgr.Wait() }()
	select {
	case err := <-werr:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server goroutine did not exit after drain")
	}
}

func TestDrainTimeout(t *testing.T) {
	t.Setenv("GRACEFUL_SHUTDOWN_TIMEOUT", "")
	assert.Equal(t, defaultDrainTimeout, drainTimeout())

	t.Setenv("GRACEFUL_SHUTDOWN_TIMEOUT", "5s")
	assert.Equal(t, 5*time.Second, drainTimeout())

	t.Setenv("GRACEFUL_SHUTDOWN_TIMEOUT", "garbage")
	assert.Equal(t, defaultDrainTimeout, drainTimeout())
}

// TestBindAddrFromEnv pins the shared bind-address resolution every component
// uses for its metrics/health servers (formerly per-package bindAddr copies).
func TestBindAddrFromEnv(t *testing.T) {
	t.Setenv("METRICS_ADDR", "")
	assert.Equal(t, ":8080", BindAddrFromEnv("METRICS_ADDR", 8080))

	t.Setenv("METRICS_ADDR", "9090")
	assert.Equal(t, ":9090", BindAddrFromEnv("METRICS_ADDR", 8080))

	t.Setenv("METRICS_ADDR", "0.0.0.0:9090")
	assert.Equal(t, "0.0.0.0:9090", BindAddrFromEnv("METRICS_ADDR", 8080))

	t.Setenv("METRICS_ADDR", "0")
	assert.Equal(t, ":0", BindAddrFromEnv("METRICS_ADDR", 8080))
}

// TestServeInjectedListener pins the listener-injection path: a caller-bound
// 127.0.0.1:0 listener is served directly (no second bind), so harnesses can
// let the kernel assign ports instead of pre-picking them.
func TestServeInjectedListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	mgr := &errgroup.Group{}
	m := httpmux.New()
	m.Handle("/ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "pong")
	}))
	go Serve(ctx, logr.Discard(), mgr, ServerOptions{Name: "test", Listener: l, Handler: m.Handler()})

	var body string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		resp, err := http.Get(fmt.Sprintf("http://%s/ping", l.Addr()))
		if !assert.NoError(c, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(c, err) {
			return
		}
		body = string(b)
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, "pong", body)
	cancel()
	_ = mgr.Wait()
}
