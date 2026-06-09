// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package mcp implements the fission-bundle --mcpPort subsystem: a Model Context
// Protocol (MCP) server that exposes opted-in Functions as tools. It watches
// Function CRDs, advertises those with Tool.ExposeAsMCP over the MCP Streamable
// HTTP transport, and proxies tools/call to the router internal listener with
// the existing ServiceRouterInternal HMAC signing. It is read-only against the
// cluster (plus best-effort status conditions) and adds no new data path.
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/httpserver"
)

// Start runs the MCP server subsystem. It builds a non-leader-elected,
// namespace-scoped cache manager over Functions (every replica serves the full
// tool list, so reconcile must run on each), registers the tool reconciler, and
// serves the MCP endpoint on port until ctx is cancelled.
//
// routerInternalURL is the resolved ROUTER_INTERNAL_URL passed down from
// fission-bundle (the same value kubewatcher/timer/mqt receive), so library
// constructors stay deterministic for unit tests.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger,
	mgr *errgroup.Group, port int, routerInternalURL string) error {
	logger = logger.WithName("mcp")

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

	// Read secrets here (not in library constructors) per the deterministic
	// constructor convention. An empty signing key puts authz in dev pass-through
	// mode; an empty HMAC master leaves outbound calls unsigned.
	authz := NewAuthorizer([]byte(os.Getenv("JWT_SIGNING_KEY")))
	proxy := NewProxy(routerInternalURL, storagesvcClient.HMACSecretFromEnv(), logger)
	registry := NewRegistry()
	server := NewServer(registry, proxy, authz, logger)

	// No leader election: each replica maintains its own in-memory registry and
	// serves tools/list from it, so each must run the reconciler. The cache is
	// scoped to the Fission-watched namespaces (per-namespace RBAC Roles forbid a
	// cluster-wide watch), mirroring the router manager.
	crMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme.Scheme,
		Cache:                  crmanager.FissionCacheOptions(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Logger:                 logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up mcp manager: %w", err)
	}

	r := &FunctionToolReconciler{
		logger: logger.WithName("function_tool_reconciler"),
		client: crMgr.GetClient(),
		reg:    registry,
		server: server,
	}
	if err := controller.Register(crMgr, &fv1.Function{}, r, "mcp-function"); err != nil {
		return fmt.Errorf("error registering mcp function reconciler: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/mcp", server.HTTPHandler())
	mgr.Go(func() error {
		httpserver.StartServer(ctx, logger, mgr, "mcp", strconv.Itoa(port), mux)
		return nil
	})

	logger.Info("starting mcp server", "port", port, "authEnabled", authz.Enabled())
	return crMgr.Start(ctx)
}
