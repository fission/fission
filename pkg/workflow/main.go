// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package workflow implements the fission-bundle --workflowPort subsystem:
// the RFC-0022 durable workflow engine. See package documentation in
// events.go for the protocol; this file is the head wiring.
package workflow

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

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
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// wakeBuffer bounds the append-then-wake channel; a dropped wake is healed
// by the 60s resync, so blocking a worker on it would be the worse trade.
const wakeBuffer = 1024

// Options configures Start; the listener is either pre-bound by the caller
// (test harness) or bound here from Port.
type Options struct {
	Port              int
	Listener          net.Listener
	RouterInternalURL string
}

// Start runs the workflow engine head: two reconcilers (WorkflowRun engine +
// Workflow validation status) on one manager, the timer lease loop, and the
// health/history listener. Single replica in v1 — correctness never depends
// on it (CAS is the arbiter), so leader election is off.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger,
	mgr *errgroup.Group, opts Options) error {
	logger = logger.WithName("workflow")

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
	// convention): the statestore DSN and the internal-auth master secret.
	opened, err := statestore.Open(ctx, statestore.FromEnv())
	if err != nil {
		return fmt.Errorf("opening statestore: %w", err)
	}
	caps := statestore.NewScoped(opened, nil)
	defer func() { _ = caps.Close() }()

	el, err := caps.EventLog()
	if err != nil {
		return fmt.Errorf("statestore EventLog capability: %w", err)
	}
	q, err := caps.Queue()
	if err != nil {
		return fmt.Errorf("statestore Queue capability: %w", err)
	}
	kv, err := caps.KV()
	if err != nil {
		return fmt.Errorf("statestore KV capability: %w", err)
	}

	// The wake channel turns worker/timer appends into immediate reconciles
	// (no CR change happens on an append, so without it a run would only
	// progress on the periodic resync). Non-blocking send: a dropped wake
	// heals on resync.
	wakeCh := make(chan event.TypedGenericEvent[client.Object], wakeBuffer)
	wake := func(key types.NamespacedName) {
		ev := event.TypedGenericEvent[client.Object]{Object: &fv1.WorkflowRun{
			ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name},
		}}
		select {
		case wakeCh <- ev:
		default:
		}
	}

	// Invocations go to the router internal listener, signed exactly like the
	// timer/mqtrigger publishers.
	var rt http.RoundTripper = otelhttp.NewTransport(httpx.PooledTransport(defaultInvokerWorkers))
	if master := storagesvcClient.HMACSecretFromEnv(); len(master) > 0 {
		rt = hmacauth.ServiceSigner(master, hmacauth.ServiceRouterInternal, rt, time.Now)
	}
	invoker := NewInvoker(InvokerOptions{
		Logger:    logger.WithName("invoker"),
		Client:    &http.Client{Transport: rt},
		RouterURL: opts.RouterInternalURL,
		EventLog:  el, KV: kv, Wake: wake, BaseCtx: ctx,
	})
	engine := NewEngine(EngineOptions{
		Logger: logger.WithName("engine"), EventLog: el, Queue: q, KV: kv,
		Invoker: invoker, Wake: wake,
	})

	crMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme.Scheme,
		Cache:                  crmanager.FissionCacheOptions(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Logger:                 logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up workflow manager: %w", err)
	}

	runReconciler := &WorkflowRunReconciler{
		logger: logger.WithName("workflowrun_reconciler"),
		client: crMgr.GetClient(),
		engine: engine,
	}
	// Cancel arrives as an annotation (no Generation bump), so the run
	// controller composes the annotation predicate; the wake channel is the
	// append-then-enqueue path.
	err = controller.RegisterTenantScopedWithRawSources(crMgr, &fv1.WorkflowRun{}, runReconciler, "workflowrun", 0,
		[]source.TypedSource[reconcile.Request]{source.Channel(wakeCh, &handler.EnqueueRequestForObject{})},
		predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))
	if err != nil {
		return fmt.Errorf("error registering workflowrun reconciler: %w", err)
	}

	wfReconciler := &WorkflowReconciler{
		logger: logger.WithName("workflow_reconciler"),
		client: crMgr.GetClient(),
	}
	if err := controller.RegisterTenantScoped(crMgr, &fv1.Workflow{}, wfReconciler, "workflow"); err != nil {
		return fmt.Errorf("error registering workflow reconciler: %w", err)
	}

	// The timer loop runs as a manager runnable so it starts after cache sync
	// and stops with the manager.
	if err := crMgr.Add(manager.RunnableFunc(engine.TimerLoop)); err != nil {
		return fmt.Errorf("adding timer loop: %w", err)
	}

	// Readiness = cache synced AND the statestore answers: a replica that
	// cannot reach its log must not be serviced.
	var cacheSynced atomic.Bool
	if err := crMgr.Add(manager.RunnableFunc(func(rctx context.Context) error {
		cacheSynced.Store(true)
		logger.Info("workflow caches synced; engine running")
		<-rctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("adding readiness runnable: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if cacheSynced.Load() && caps.Ping(r.Context()) == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	registerHistoryAPI(mux, logger, el, kv, storagesvcClient.HMACSecretFromEnv())

	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{
			Name: "workflow", Addr: strconv.Itoa(opts.Port), Listener: opts.Listener, Handler: mux,
		})
		return nil
	})

	logger.Info("starting workflow engine", "port", opts.Port)
	return crMgr.Start(ctx)
}

// GetLogger keeps parity with sibling heads for the bundle dispatcher.
func GetLogger() logr.Logger { return loggerfactory.GetLogger() }
