package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/utils/manager"
)

func StartServer(ctx context.Context, log logr.Logger, mgr manager.Interface, svc string, port string, handler http.Handler) {
	if !strings.Contains(port, ":") {
		port = fmt.Sprintf(":%s", port)
	}
	server := http.Server{
		Addr:    port,
		Handler: handler,
	}
	l := log.WithValues("service", svc, "addr", server.Addr)
	l.Info("starting server")
	mgr.Add(ctx, func(ctx context.Context) {
		if err := server.ListenAndServe(); err != nil {
			if err != http.ErrServerClosed {
				l.Error(err, "server error")
			}
		}
	})
	<-ctx.Done()
	l.Info("shutting down server")
	if err := server.Shutdown(ctx); err != nil {
		if err != context.Canceled && err != context.DeadlineExceeded {
			l.Error(err, "server shutdown error")
		}
	}
}
