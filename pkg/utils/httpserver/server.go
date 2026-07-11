// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"context"
	"fmt"
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

func StartServer(ctx context.Context, log logr.Logger, mgr *errgroup.Group, svc string, port string, handler http.Handler) {
	if !strings.Contains(port, ":") {
		port = fmt.Sprintf(":%s", port)
	}
	server := http.Server{
		Addr:    port,
		Handler: handler,
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
	l := log.WithValues("service", svc, "addr", server.Addr)
	l.Info("starting server")
	mgr.Go(func() error {
		if err := server.ListenAndServe(); err != nil {
			if err != http.ErrServerClosed {
				l.Error(err, "server error")
			}
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
