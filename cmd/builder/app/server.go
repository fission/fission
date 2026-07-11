// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"net/http"
	"os"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	builder "github.com/fission/fission/pkg/builder"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/httpserver"

	"strconv"

	"github.com/fission/fission/pkg/svcinfo"
)

// Usage: builder <shared volume path>
func Run(ctx context.Context, logger logr.Logger, mgr *errgroup.Group, shareVolume string) {
	builder := builder.MakeBuilder(logger, shareVolume)
	mux := http.NewServeMux()
	mux.HandleFunc("/", builder.Handler)
	mux.HandleFunc("/clean", builder.Clean)
	mux.HandleFunc("/version", builder.VersionHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Wrap the mux with the HMAC verifier middleware. The master
	// secret (when set via FISSION_INTERNAL_AUTH_SECRET on the builder
	// pod) is derived per-service for ServiceBuilder so a leak of this
	// builder's runtime memory cannot forge requests on other Fission
	// internal channels (storagesvc, fetcher, executor,
	// router-internal). An empty master means the underlying Verifier
	// short-circuits to pass-through, preserving backwards
	// compatibility for installs with internalAuth disabled. /healthz
	// is bypassed so kubelet probes continue to pass without signing.
	// See docs/internal-auth/00-design.md.
	vopts := hmacauth.VerifierOpts{
		SkewSec:      60,
		Bypass:       []string{"/healthz"},
		MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
		Logger:       logger.WithName("hmac"),
	}
	// Per-namespace tenancy: when the tenant controller has mounted a derived
	// builder key (FISSION_BUILDER_KEY), verify /build with it directly — this pod
	// then never holds the master, so a leak of its memory cannot forge requests
	// as another tenant's builder. Otherwise fall back to deriving ServiceBuilder
	// from the master (existing behaviour; empty master = pass-through).
	verifier := hmacauth.VerifierFromKeyOrMaster(
		hmacauth.DecodeKeyFromEnv(os.Getenv("FISSION_BUILDER_KEY")),
		hmacauth.DecodeKeyFromEnv(os.Getenv("FISSION_BUILDER_KEY_OLD")),
		[]byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET")),
		[]byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD")),
		hmacauth.ServiceBuilder, vopts)
	// Builder is a pod-local sidecar with no Service; no legitimate
	// browser caller. SecurityHeaders + DenyAllCORS as defense-in-depth.
	handler := httpsecurity.SecurityHeaders(httpsecurity.DenyAllCORS(verifier(mux)))
	httpserver.StartServer(ctx, logger, mgr, "builder", strconv.Itoa(svcinfo.PortBuilder), handler)
}
