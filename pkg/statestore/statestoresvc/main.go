// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package statestoresvc implements the fission-bundle --statestorePort subsystem:
// the embedded-mode statestore. A single replica owns a PVC-backed SQLite file
// and serves the RFC-0021 capability API (pkg/statestore/httpapi) over a
// ClusterIP-only Service, authenticated with the ServiceStatestore HMAC key like
// the other internal listeners. It is deliberately single-writer and not HA.
package statestoresvc

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/httpapi"

	// Register the embedded SQLite driver so statestore.Open(sqlite) resolves.
	_ "github.com/fission/fission/pkg/statestore/sqlite"
	"github.com/fission/fission/pkg/utils/httpserver"
)

// maxBodyBytes caps request bodies the HMAC verifier buffers. A KV value is
// capped at 256KiB (RFC-0023); base64 + the JSON envelope inflate that, so 4MiB
// leaves generous headroom.
const maxBodyBytes = 4 << 20

// Options configures Start. The listener is either pre-bound by the caller
// (Listener — e.g. a test harness binding 127.0.0.1:0) or bound from Port.
type Options struct {
	// Port is the capability API port. Ignored when Listener is set.
	Port int
	// Listener optionally pre-binds the listener.
	Listener net.Listener
	// Caps optionally injects a pre-opened Capabilities (tests). When nil, Start
	// opens the embedded SQLite store at STATESTORE_DSN.
	Caps statestore.Capabilities
}

// Start runs the embedded statestore subsystem, serving the capability API until
// ctx is cancelled. Secrets are read here (not in library constructors) per the
// deterministic-constructor convention.
func Start(ctx context.Context, _ crd.ClientGeneratorInterface, logger logr.Logger,
	mgr *errgroup.Group, opts Options) error {
	logger = logger.WithName("statestore")

	caps := opts.Caps
	if caps == nil {
		dsn := os.Getenv("STATESTORE_DSN")
		if dsn == "" {
			return fmt.Errorf("statestore: STATESTORE_DSN is required in embedded mode (the SQLite file path)")
		}
		opened, err := statestore.Open(ctx, statestore.Config{Driver: "sqlite", DSN: dsn})
		if err != nil {
			return fmt.Errorf("statestore: opening embedded store: %w", err)
		}
		caps = opened
	}

	handler := httpapi.NewHandler(caps)

	// HMAC-verify the capability API on the internal listener (empty master =
	// pass-through, matching the router-internal convention). /healthz and /readyz
	// stay unauthenticated so the kubelet can probe them.
	master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	masterOld := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD"))
	if len(master) > 0 {
		verifier := hmacauth.ServiceVerifier(master, masterOld, hmacauth.ServiceStatestore, hmacauth.VerifierOpts{
			SkewSec:      60,
			MaxBodyBytes: maxBodyBytes,
			Bypass:       []string{httpapi.PathHealthz, httpapi.PathReadyz},
			Logger:       logger,
		})
		handler = verifier(handler)
		logger.Info("embedded statestore HMAC verification enabled")
	} else {
		logger.Info("WARNING: embedded statestore HMAC verification DISABLED (empty FISSION_INTERNAL_AUTH_SECRET)")
	}

	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{
			Name: "statestore", Addr: strconv.Itoa(opts.Port), Listener: opts.Listener, Handler: handler,
		})
		return nil
	})
	logger.Info("starting embedded statestore", "port", opts.Port)
	<-ctx.Done()
	return nil
}
