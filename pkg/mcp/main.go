// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package mcp implements the fission-bundle --mcpPort subsystem: a Model Context
// Protocol (MCP) server that exposes opted-in Functions as tools. It watches
// Function CRDs, advertises those whose Tool is set over the MCP Streamable HTTP
// transport, and proxies tools/call to the router internal listener with the
// existing ServiceRouterInternal HMAC signing. It is read-only against the
// cluster (plus best-effort status conditions) and adds no new data path.
package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/httpserver"
)

// envAllowInsecure, when "true", permits the MCP server to start without a
// signing key (every caller gets a wildcard scope). Without it, an empty
// JWT_SIGNING_KEY is a hard startup error so the endpoint fails closed rather
// than silently serving every tool unauthenticated.
const envAllowInsecure = "MCP_ALLOW_INSECURE"

// Start runs the MCP server subsystem. It builds a non-leader-elected,
// namespace-scoped cache manager over Functions (every replica serves the full
// tool list, so reconcile must run on each), registers the tool reconciler, and
// serves the MCP endpoint on port until ctx is cancelled.
//
// Options configures Start. The listener is either pre-bound by the caller
// (Listener — e.g. a test harness binding 127.0.0.1:0) or bound here from
// Port.
type Options struct {
	// Port is the MCP server port. Ignored when Listener is set.
	Port int
	// Listener optionally pre-binds the listener.
	Listener net.Listener
	// RouterInternalURL is the resolved ROUTER_INTERNAL_URL passed down from
	// fission-bundle (the same value kubewatcher/timer/mqt receive), so
	// library constructors stay deterministic for unit tests.
	RouterInternalURL string
}

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger,
	mgr *errgroup.Group, opts Options) error {
	routerInternalURL := opts.RouterInternalURL
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

	// Fail closed: refuse to serve unauthenticated unless explicitly opted in.
	// Pass-through grants every caller a wildcard scope and can invoke any
	// Tool-exposed function via the internal listener, so it must be a deliberate
	// choice, not the consequence of a missing key.
	allowInsecure, _ := strconv.ParseBool(os.Getenv(envAllowInsecure))
	if !authz.Enabled() && !allowInsecure {
		return fmt.Errorf("refusing to start MCP server without authentication: set JWT_SIGNING_KEY to scope agent access, or %s=true to explicitly run the endpoint unauthenticated (dev only)", envAllowInsecure)
	}

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
	if err := controller.RegisterTenantScoped(crMgr, &fv1.Function{}, r, "mcp-function"); err != nil {
		return fmt.Errorf("error registering mcp function reconciler: %w", err)
	}

	// ready flips true once the Function cache has synced (the manager starts
	// added runnables only after cache sync), so a replica reports ready only
	// after its registry is being populated. Without this an agent that connects
	// during warm-up gets an empty tools/list as a *successful* response (a silent
	// wrong answer it won't retry). /healthz stays liveness-only.
	var ready atomic.Bool
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		ready.Store(true)
		logger.Info("mcp function cache synced; serving tools")
		<-rctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("adding mcp readiness runnable: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.Handle("/mcp", server.HTTPHandler())
	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{
			Name: "mcp", Addr: strconv.Itoa(opts.Port), Listener: opts.Listener, Handler: mux,
		})
		return nil
	})

	if authz.Enabled() {
		logger.Info("starting mcp server", "port", opts.Port, "authEnabled", true)
	} else {
		// Pass-through mode grants every caller a wildcard scope. Explicitly
		// opted in via MCP_ALLOW_INSECURE; loud so it is never mistaken for a
		// scoped deployment.
		logger.Info("WARNING: starting mcp server with authentication DISABLED — every caller can list and invoke all tools (set JWT_SIGNING_KEY to scope access)", "port", opts.Port)
	}
	return crMgr.Start(ctx)
}
