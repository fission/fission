// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"

	"github.com/go-logr/logr"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/fetcher"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/httpserver"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

var (
	readyToServe atomic.Uint32
)

func Run(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group, port string, podInfoMountDir string) error {
	flag.Usage = fetcherUsage
	specializeOnStart := flag.Bool("specialize-on-startup", false, "Flag to activate specialize process at pod startup")
	specializePayload := flag.String("specialize-request", "", "JSON payload for specialize request")
	secretDir := flag.String("secret-dir", "", "Path to shared secrets directory")
	configDir := flag.String("cfgmap-dir", "", "Path to shared configmap directory")

	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		return errors.New("missing arguments")
	}

	dir := flag.Arg(0)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(dir, os.ModeDir|0700)
			if err != nil {
				return fmt.Errorf("error creating directory %s: %w", dir, err)
			}
		}
	}

	shutdown, err := otelUtils.InitProvider(ctx, logger, "Fission-Fetcher")
	if err != nil {
		return fmt.Errorf("error initializing OTLP provider: %w", err)
	}
	if shutdown != nil {
		defer shutdown(ctx)
	}

	tracer := otel.Tracer("fetcher")
	ctx, span := tracer.Start(ctx, "fetcher/Run")
	defer span.End()

	f, err := fetcher.MakeFetcher(logger, clientGen, dir, *secretDir, *configDir, podInfoMountDir)
	if err != nil {
		return fmt.Errorf("error making fetcher: %w", err)
	}

	// do specialization in other goroutine to prevent blocking in newdeploy
	mgr.Go(func() error {
		if *specializeOnStart {
			var specializeReq fetcher.FunctionSpecializeRequest

			err := json.Unmarshal([]byte(*specializePayload), &specializeReq)
			if err != nil {
				logger.Error(err, "error decoding specialize request")
				return nil
			}

			code, err := f.SpecializePod(ctx, specializeReq.FetchReq, specializeReq.LoadReq)
			if err != nil {
				logger.Error(err, "error specializing function pod", "statusCode", code)
				return nil
			}
		}
		readyToServe.Store(1)
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/fetch", f.FetchHandler)
	mux.HandleFunc("/specialize", f.SpecializeHandler)
	mux.HandleFunc("/upload", f.UploadHandler)
	mux.HandleFunc("/version", f.VersionHandler)
	mux.HandleFunc("/wsevent/start", f.WsStartHandler)
	mux.HandleFunc("/wsevent/end", f.WsEndHandler)

	readinessHandler := func(w http.ResponseWriter, r *http.Request) {
		if readyToServe.Load() == 1 {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}

	mux.HandleFunc("/readiness-healthz", readinessHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	logger.Info("fetcher ready to receive requests")

	// Wrap the mux with the HMAC verifier middleware. The master
	// secret (when set via FISSION_INTERNAL_AUTH_SECRET on the function
	// pod's fetcher container) is derived per-service for
	// ServiceFetcher so a leak of this fetcher's runtime memory cannot
	// forge requests on other Fission internal channels (storagesvc,
	// builder, executor, router-internal). An empty master means the
	// underlying Verifier short-circuits to pass-through, preserving
	// backwards compatibility for installs with internalAuth disabled.
	// /healthz and /readiness-healthz are bypassed so kubelet probes
	// continue to pass without signing. See
	// docs/internal-auth/00-design.md.
	vopts := hmacauth.VerifierOpts{
		SkewSec:      60,
		Bypass:       []string{"/healthz", "/readiness-healthz"},
		MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
		Logger:       logger.WithName("hmac"),
	}
	// Per-namespace tenancy: when the tenant controller has mounted a derived
	// fetcher key (FISSION_FETCHER_KEY), verify /specialize with it directly —
	// this pod then never holds the master, so a leak of its memory cannot forge
	// requests as another tenant's fetcher. Otherwise fall back to deriving the
	// ServiceFetcher key from the master (existing behaviour; empty master =
	// pass-through).
	verifier := hmacauth.VerifierFromKeyOrMaster(
		hmacauth.DecodeKeyFromEnv(os.Getenv("FISSION_FETCHER_KEY")),
		hmacauth.DecodeKeyFromEnv(os.Getenv("FISSION_FETCHER_KEY_OLD")),
		[]byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET")),
		[]byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD")),
		hmacauth.ServiceFetcher, vopts)
	// Fetcher is a pod-local sidecar with no Service; no legitimate
	// browser caller. SecurityHeaders + DenyAllCORS as defense-in-depth
	// against a hostile package running in the same pod-network namespace.
	handler := httpsecurity.SecurityHeaders(
		httpsecurity.DenyAllCORS(
			otelUtils.GetHandlerWithOTEL(verifier(mux), "fission-fetcher", otelUtils.UrlsToIgnore("/healthz", "/readiness-healthz")),
		),
	)
	httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{Name: "fetcher", Addr: port, Handler: handler})
	return nil
}

func fetcherUsage() {
	fmt.Println("Usage: fetcher [-specialize-on-startup] [-specialize-request <json>] [-secret-dir <string>] [-cfgmap-dir <string>] <shared volume path>")
}
