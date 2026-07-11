// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// To use profile in a go program over http
// import this package and call ProfileIfEnabled()
// in your main function.
// Please set the environment variable PPROF_ENABLE=true to enable/disable it runtime.
// To customize host and port you can set PPROF_HOST and PPROF_PORT environment variables.
//	$ PPROF_ENABLE=true PPROF_HOST=localhost PPROF_PORT=6060 go run myprogram.go

package profile

import (
	"context"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	"github.com/fission/fission/pkg/utils/httpserver"
)

func ProfileIfEnabled(ctx context.Context, logger logr.Logger, mgr *errgroup.Group) {
	enablePprof := os.Getenv("PPROF_ENABLED")
	if enablePprof != "true" {
		return
	}
	pprofPort := os.Getenv("PPROF_PORT")
	if pprofPort == "" {
		pprofPort = "6060"
	}

	pprofMux := http.DefaultServeMux
	http.DefaultServeMux = http.NewServeMux()

	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{Name: "pprof", Addr: pprofPort, Handler: pprofMux})
		return nil
	})
}
