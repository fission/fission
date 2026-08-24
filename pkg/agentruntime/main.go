// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package agentruntime implements the fission-bundle --agentPort subsystem:
// session dispatch, a per-replica registry of agent-enabled Functions, and
// an idle/archive sweeper for their sessions. It watches Function CRDs
// (Spec.Agent != nil), resolves an inbound "/agents/{namespace}/{name}" turn
// to the matching Function, forwards it to the router internal listener with
// the same ServiceRouterInternal HMAC signing the other publishers use, and
// meters the result into a per-session record. In v1, only Content-Type, the
// session header, and the runtime's own turn/wake headers (X-Fission-Agent-
// Turns, and X-Fission-Agent-Wake-Id/-Attempt on wake-delivered turns) are
// forwarded upstream, and only Content-Type plus X-Fission-Agent-Yield are
// returned; custom caller headers are intentionally not proxied in v1
// (revisit with the SDK slice).
//
// Sessions are statestore records, never CRDs — there is no AgentSession CRD
// and no reconcile loop over session state. The sweeper that ages sessions
// active -> idle -> archived runs on every replica with no leader election:
// every transition is a CAS write at the version the sweeper (or the request
// dispatcher) just read, so two replicas racing the same record simply have
// one CAS win and one CAS lose silently — that idempotence is the substitute
// for leader election, the same way the router-admitted request path never
// needed one.
//
// Known follow-ups (not in v1): no status condition is written back to the
// Function (unlike mcp's ToolExposed) — once the agent runtime has an
// observable signal worth surfacing, add an AgentReady/AgentExposed
// condition the same way pkg/mcp/reconciler.go's controller.SetConditions
// calls do. authz.go is a hand-mirrored duplicate of pkg/mcp/authz.go rather
// than a shared package; extracting the common bearer-token verifier into
// one place both subsystems import is deferred cleanup, not a design choice
// worth blocking this slice on.
package agentruntime

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/client"   // embedded-mode driver
	_ "github.com/fission/fission/pkg/statestore/memory"   // dev/test driver
	_ "github.com/fission/fission/pkg/statestore/postgres" // external-mode driver
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/httpx"
)

const (
	// envAllowInsecure, when "true", permits the agent runtime to start
	// without a signing key (every caller gets a wildcard namespace scope).
	// Without it, an empty JWT_SIGNING_KEY is a hard startup error so the
	// endpoint fails closed rather than silently dispatching every caller's
	// turns unauthenticated.
	envAllowInsecure = "AGENT_ALLOW_INSECURE"

	// envSweepInterval / envArchiveRetention configure the Sweeper. Both are
	// read once here (never in a library constructor) per the deterministic-
	// constructor convention.
	envSweepInterval    = "AGENT_SWEEP_INTERVAL"
	envArchiveRetention = "AGENT_ARCHIVE_RETENTION"

	// envMaxContinuations configures the Dispatcher's yield=continue
	// self-chaining cap. 0 means unlimited (see Dispatcher.maxContinuations).
	envMaxContinuations = "AGENT_MAX_CONTINUATIONS"

	// defaultSweepInterval / defaultArchiveRetention are applied when the
	// corresponding env var is unset.
	defaultSweepInterval    = 30 * time.Second
	defaultArchiveRetention = 168 * time.Hour // 7 days

	// defaultMaxContinuations is applied when envMaxContinuations is unset.
	defaultMaxContinuations = 1000

	// dispatcherMaxIdleConnsPerHost bounds the dispatcher's outbound
	// connection pool to the single router-internal host (see
	// httpx.PooledTransport) — sized like the workflow invoker's worker pool,
	// since both are internal clients driving one hot upstream.
	dispatcherMaxIdleConnsPerHost = 64
)

// Options configures Start. The listener is either pre-bound by the caller
// (Listener — e.g. a test harness binding 127.0.0.1:0) or bound here from
// Port.
type Options struct {
	// Port is the agent runtime's port. Ignored when Listener is set.
	Port int
	// Listener optionally pre-binds the listener.
	Listener net.Listener
	// RouterInternalURL is the resolved ROUTER_INTERNAL_URL passed down from
	// fission-bundle (the same value kubewatcher/timer/mqt/mcp receive), so
	// library constructors stay deterministic for unit tests.
	RouterInternalURL string
}

// Start runs the agent runtime subsystem. It builds a non-leader-elected,
// namespace-scoped cache manager over Functions (every replica serves its own
// AgentView, so reconcile must run on each), registers the agent reconciler,
// and serves the dispatch endpoint on Port until ctx is cancelled.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger,
	mgr *errgroup.Group, opts Options) error {
	logger = logger.WithName("agentruntime")

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

	// Env reads live here, never in constructors (deterministic-constructor
	// convention): the statestore DSN, the sweep/retention policy, and the
	// internal-auth master secret.
	opened, err := statestore.Open(ctx, statestore.FromEnv())
	if err != nil {
		return fmt.Errorf("opening statestore: %w", err)
	}
	caps := statestore.NewScoped(opened, nil)
	defer func() { _ = caps.Close() }()

	kv, err := caps.KV()
	if err != nil {
		return fmt.Errorf("statestore KV capability: %w", err)
	}
	queue, err := caps.Queue()
	if err != nil {
		return fmt.Errorf("statestore Queue capability: %w", err)
	}

	sweepInterval, err := envDuration(envSweepInterval, defaultSweepInterval)
	if err != nil {
		return err
	}
	retention, err := envDuration(envArchiveRetention, defaultArchiveRetention)
	if err != nil {
		return err
	}
	maxContinuations, err := envInt64(envMaxContinuations, defaultMaxContinuations)
	if err != nil {
		return err
	}

	// Fail closed: refuse to serve unauthenticated unless explicitly opted
	// in. Pass-through grants every caller a wildcard namespace scope and can
	// dispatch a turn to any agent-enabled Function via the internal
	// listener, so it must be a deliberate choice, not the consequence of a
	// missing key.
	authz := NewAuthorizer([]byte(os.Getenv("JWT_SIGNING_KEY")))
	allowInsecure, _ := strconv.ParseBool(os.Getenv(envAllowInsecure))
	if !authz.Enabled() && !allowInsecure {
		return fmt.Errorf("refusing to start agent runtime without authentication: set JWT_SIGNING_KEY to scope agent access, or %s=true to explicitly run the endpoint unauthenticated (dev only)", envAllowInsecure)
	}

	// Invocations go to the router internal listener, signed exactly like the
	// other publishers (timer/mqtrigger/workflow/mcp).
	var rt http.RoundTripper = otelhttp.NewTransport(httpx.PooledTransport(dispatcherMaxIdleConnsPerHost))
	if master := storagesvcClient.HMACSecretFromEnv(); len(master) > 0 {
		rt = hmacauth.ServiceSigner(master, hmacauth.ServiceRouterInternal, rt, time.Now)
	}

	view := NewAgentView()
	store := NewSessionStore(kv, time.Now)
	dispatcher := NewDispatcher(logger.WithName("dispatcher"), view, store, &http.Client{Transport: rt}, opts.RouterInternalURL, retention, time.Now, maxContinuations)
	sweeper := NewSweeper(logger.WithName("sweeper"), view, store, sweepInterval, retention, time.Now)

	// WakeService needs the Dispatcher (to call DispatchTurn), so it is built
	// after the Dispatcher and wired back in via SetWakeEnqueuer — see that
	// method's doc comment for the construction-cycle rationale. This MUST
	// happen before the mux starts serving turns.
	wake := NewWakeService(logger.WithName("wake"), queue, dispatcher, time.Now)
	wake.SetRand(rand.Float64)
	dispatcher.SetWakeEnqueuer(wake)

	// No leader election: each replica maintains its own in-memory view and
	// serves dispatch from it, so each must run the reconciler. The cache is
	// scoped to the Fission-watched namespaces (per-namespace RBAC Roles
	// forbid a cluster-wide watch), mirroring the mcp/router managers.
	crMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme.Scheme,
		Cache:                  crmanager.FissionCacheOptions(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Logger:                 logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up agentruntime manager: %w", err)
	}

	r := NewAgentReconciler(logger.WithName("agent_reconciler"), crMgr.GetClient(), view)
	if err := controller.RegisterTenantScoped(crMgr, &fv1.Function{}, r, "agentruntime-function"); err != nil {
		return fmt.Errorf("error registering agentruntime function reconciler: %w", err)
	}

	// The sweeper runs as a manager runnable so it starts after cache sync
	// (a replica's view is empty until then anyway) and stops with the
	// manager.
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		sweeper.Run(rctx)
		return nil
	})); err != nil {
		return fmt.Errorf("adding agentruntime sweeper: %w", err)
	}

	// The wake service runs as a manager runnable for the same reason the
	// sweeper does (start after cache sync, stop with the manager); it
	// delivers the yield=continue self-chain the dispatcher enqueues via
	// SetWakeEnqueuer above.
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		wake.Run(rctx)
		return nil
	})); err != nil {
		return fmt.Errorf("adding agentruntime wake service: %w", err)
	}

	// ready flips true once the Function cache has synced (the manager starts
	// added runnables only after cache sync), so a replica reports ready only
	// after its view is being populated. /readyz additionally requires the
	// statestore to answer at probe time — a replica that cannot reach it
	// must not be serviced.
	var ready atomic.Bool
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		ready.Store(true)
		logger.Info("agentruntime function cache synced; dispatching turns")
		<-rctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("adding agentruntime readiness runnable: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "caches not yet synced", http.StatusServiceUnavailable)
			return
		}
		if err := caps.Ping(r.Context()); err != nil {
			// The body says WHY: an opaque 503 turns into a rollout-timeout
			// mystery in CI.
			http.Error(w, "statestore unreachable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	// The pattern here matches Dispatcher.Handler()'s own registration
	// exactly: the outer mux performs the {namespace}/{name} match (and sets
	// the path values authz.Middleware reads) before calling into the
	// auth-wrapped dispatcher, which then matches the same pattern again.
	mux.Handle("POST /agents/{namespace}/{name}", authz.Middleware(dispatcher.Handler()))

	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{
			Name: "agentruntime", Addr: strconv.Itoa(opts.Port), Listener: opts.Listener, Handler: mux,
		})
		return nil
	})

	if authz.Enabled() {
		logger.Info("starting agentruntime server", "port", opts.Port, "authEnabled", true)
	} else {
		// Pass-through mode grants every caller a wildcard namespace scope.
		// Explicitly opted in via AGENT_ALLOW_INSECURE; loud so it is never
		// mistaken for a scoped deployment.
		logger.Info("WARNING: starting agentruntime server with authentication DISABLED — every caller can dispatch turns to any agent-enabled function (set JWT_SIGNING_KEY to scope access)", "port", opts.Port)
	}
	return crMgr.Start(ctx)
}

// envDuration reads key as a time.Duration, returning def when it is unset.
// A set-but-unparseable value is a startup error, not a silent fallback to
// def — a typo'd AGENT_SWEEP_INTERVAL should fail loudly, not run the
// sweeper on a default cadence nobody chose.
func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("parsing %s=%q: %w", key, v, err)
	}
	return d, nil
}

// envInt64 reads key as an int64, returning def when it is unset. A
// set-but-unparseable value is a startup error, not a silent fallback to def
// — mirrors envDuration's rationale.
func envInt64(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s=%q: %w", key, v, err)
	}
	return n, nil
}
