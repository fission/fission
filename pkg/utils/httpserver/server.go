// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
)

// defaultDrainTimeout bounds how long a server waits for in-flight requests to
// complete during graceful shutdown. Overridable via GRACEFUL_SHUTDOWN_TIMEOUT
// (any time.ParseDuration value). Keep it at or below the pod's
// terminationGracePeriodSeconds so the drain finishes before SIGKILL.
const defaultDrainTimeout = 30 * time.Second

func drainTimeout() time.Duration {
	if v := os.Getenv("GRACEFUL_SHUTDOWN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultDrainTimeout
}

// BindAddrFromEnv resolves a listen address from an env var, defaulting to
// defPort, and prefixes ":" when only a port is given. Shared by every
// component that binds a metrics/health server (previously each carried a
// private bindAddr copy).
func BindAddrFromEnv(env string, defPort int) string {
	addr := os.Getenv(env)
	if addr == "" {
		addr = strconv.Itoa(defPort)
	}
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	return addr
}

// ServerOptions configures Serve. Exactly one of Addr or Listener supplies
// the bind: a non-nil Listener takes precedence (the caller pre-bound it —
// e.g. a test harness on 127.0.0.1:0), otherwise Addr is bound here.
type ServerOptions struct {
	// Name identifies the service in logs.
	Name string
	// Addr is the listen address; a bare port ("8888") is prefixed with ":".
	// Ignored when Listener is set.
	Addr string
	// Listener is an optional pre-bound listener the server serves on. The
	// server takes ownership and closes it on shutdown.
	Listener net.Listener
	// Handler is the HTTP handler to serve.
	Handler http.Handler
}

// Serve runs an HTTP server until ctx is cancelled, then drains in-flight
// requests (bounded by GRACEFUL_SHUTDOWN_TIMEOUT, default 30s) before
// returning.
func Serve(ctx context.Context, log logr.Logger, mgr *errgroup.Group, opts ServerOptions) {
	addr := opts.Addr
	if !strings.Contains(addr, ":") {
		addr = fmt.Sprintf(":%s", addr)
	}
	server := http.Server{
		Addr:    addr,
		Handler: opts.Handler,
		// ReadHeaderTimeout bounds only the header read (slowloris hardening);
		// request bodies and long-running/streaming responses are unaffected —
		// deliberately no ReadTimeout/WriteTimeout, which would cut both.
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout reaps idle keep-alive connections. It must exceed the
		// 90s IdleConnTimeout of http.DefaultTransport (which the internal
		// pooled clients inherit): if the server closed first, a client could
		// reuse a connection the server is tearing down and see spurious
		// failures on non-idempotent internal POSTs.
		IdleTimeout: 120 * time.Second,
	}
	displayAddr := server.Addr
	if opts.Listener != nil {
		displayAddr = opts.Listener.Addr().String()
	}
	l := log.WithValues("service", opts.Name, "addr", displayAddr)
	l.Info("starting server")
	mgr.Go(func() error {
		var err error
		if opts.Listener != nil {
			err = server.Serve(opts.Listener)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			l.Error(err, "server error")
		}
		return nil
	})
	<-ctx.Done()
	// ctx is already cancelled here (that is why we woke up), so it cannot be
	// used to bound the drain — Shutdown(ctx) would return immediately and cut
	// in-flight requests. Use a fresh timeout context so requests get a window
	// to complete before connections are closed.
	timeout := drainTimeout()
	l.Info("shutting down server", "drainTimeout", timeout)
	drainCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(drainCtx); err != nil {
		if err != context.Canceled && err != context.DeadlineExceeded {
			l.Error(err, "server shutdown error")
		}
	}
}
