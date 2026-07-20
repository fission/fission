// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package statesvc implements the fission-bundle --stateApiPort subsystem: the
// RFC-0023 function-facing keyed-state head. It is a SCOPED surface over the
// RFC-0021 statestore — every request's KV access goes through the shipped
// scoped wrapper with the Scope derived from a verified per-keyspace token,
// never from the request alone — deliberately separate from the raw
// statestoresvc head, which serves the unscoped substrate to control-plane
// drivers and must never be reachable from function pods.
package statesvc

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/httpserver"
)

// Options configures Start. The listener is either pre-bound by the caller
// (Listener — e.g. a test harness binding 127.0.0.1:0) or bound here from
// Port. Caps optionally injects a pre-opened store (tests); when nil the
// driver comes from STATESTORE_DRIVER/STATESTORE_DSN (the client driver
// pointed at statestoresvc in the chart's embedded mode).
type Options struct {
	Port     int
	Listener net.Listener
	Caps     statestore.Capabilities
}

// Start runs the statesvc head: a non-leader-elected Function-informed
// manager (every replica needs its own FunctionIndex, like mcp) plus the
// authenticated state API. /readyz gates on the Function cache sync AND a
// store ping, so a warming replica is never added to the Service.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger,
	mgr *errgroup.Group, opts Options) error {
	logger = logger.WithName("statesvc")

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
	}
	if err := crd.WaitForFunctionCRDs(ctx, logger, fissionClient); err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	caps := opts.Caps
	if caps == nil {
		caps, err = statestore.Open(ctx, statestore.FromEnv())
		if err != nil {
			return fmt.Errorf("opening statestore: %w", err)
		}
		defer func() { _ = caps.Close() }()
	}

	index := NewFunctionIndex()
	scoped := statestore.NewScoped(caps, index)
	kv, err := scoped.KV()
	if err != nil {
		return fmt.Errorf("statestore KV capability: %w", err)
	}

	// Secrets are read here (not in library constructors) per the
	// deterministic-constructor convention. Empty master = bearer pass-through
	// (dev only; the chart always provisions the secret) and a fail-closed
	// admin path.
	master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	masterOld := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD"))
	auth := newAuthenticator(master, masterOld, hmacauth.VerifierOpts{
		SkewSec:      60,
		MaxBodyBytes: fv1.DefaultStateMaxValueBytes * 2,
		Logger:       logger,
	})
	if auth.passThrough() {
		logger.Info("WARNING: starting statesvc without FISSION_INTERNAL_AUTH_SECRET — keyspace tokens are NOT verified and the admin path is disabled (dev only)")
	}

	crMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme.Scheme,
		Cache:                  crmanager.FissionCacheOptions(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Logger:                 logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up statesvc manager: %w", err)
	}

	r := &functionStateReconciler{
		logger: logger.WithName("function_state_reconciler"),
		client: crMgr.GetClient(),
		index:  index,
		kv:     kv,
	}
	if err := controller.RegisterTenantScopedWithPredicates(crMgr, &fv1.Function{}, r, "statesvc-function", 0,
		predicate.Or(predicate.GenerationChangedPredicate{}, deletionTimestampPredicate)); err != nil {
		return fmt.Errorf("error registering statesvc function reconciler: %w", err)
	}

	// cacheSynced flips once the manager starts runnables (after cache sync):
	// serving before the index is populated would 403 every known keyspace.
	var cacheSynced atomic.Bool
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		cacheSynced.Store(true)
		logger.Info("statesvc function cache synced; serving state API")
		<-rctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("adding statesvc readiness runnable: %w", err)
	}
	ready := func() bool {
		return cacheSynced.Load() && scoped.Ping(ctx) == nil
	}

	handler := newHandler(kv, index, auth, ready, logger)
	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{
			Name: "statesvc", Addr: strconv.Itoa(opts.Port), Listener: opts.Listener, Handler: handler,
		})
		return nil
	})

	logger.Info("starting statesvc", "port", opts.Port, "authEnabled", !auth.passThrough())
	return crMgr.Start(ctx)
}
