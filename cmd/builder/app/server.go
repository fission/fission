/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package app

import (
	"context"
	"net/http"
	"os"

	"github.com/go-logr/logr"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	builder "github.com/fission/fission/pkg/builder"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/manager"
)

// Usage: builder <shared volume path>
func Run(ctx context.Context, logger logr.Logger, mgr manager.Interface, shareVolume string) {
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
	master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	masterOld := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD"))
	verifier := hmacauth.ServiceVerifier(master, masterOld, hmacauth.ServiceBuilder, hmacauth.VerifierOpts{
		SkewSec:      60,
		Bypass:       []string{"/healthz"},
		MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
		Logger:       logger.WithName("hmac"),
	})
	// Builder is a pod-local sidecar with no Service; no legitimate
	// browser caller. SecurityHeaders + DenyAllCORS as defense-in-depth.
	handler := httpsecurity.SecurityHeaders(httpsecurity.DenyAllCORS(verifier(mux)))
	httpserver.StartServer(ctx, logger, mgr, "builder", "8001", handler)
}
