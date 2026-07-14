// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bep/debounce"
	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	ferror "github.com/fission/fission/pkg/error"
	eclient "github.com/fission/fission/pkg/executor/client"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/info"
	"github.com/fission/fission/pkg/router/routetable"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/httpsecurity"
)

// HTTPTriggerSet represents an HTTP trigger set
type HTTPTriggerSet struct {
	*mutableRouter

	// internalMutableRouter is the mutable wrapper for the internal listener.
	// It is updated in lockstep with mutableRouter (the public listener) on
	// every trigger / function reconciliation. May be nil in unit-test paths
	// that only construct the public mux directly.
	internalMutableRouter *mutableRouter

	logger        logr.Logger
	fissionClient versioned.Interface
	kubeClient    kubernetes.Interface
	// client is the Manager's cache-backed client. The trigger/function
	// reconcilers and the incremental resync read HTTPTriggers + Functions
	// through it, replacing the per-namespace SharedIndexInformers the router
	// used before the controller-runtime migration.
	client   client.Client
	resolver *functionReferenceResolver
	// addressResolver and tapper are the proxy path's injected seams
	// (RFC-0002): function→address resolution and tap/untap accounting. The
	// executor client, address cache, and throttler live inside them —
	// deliberately NOT also retained on this struct, so there is exactly one
	// live copy of that state.
	addressResolver            AddressResolver
	tapper                     Tapper
	triggers                   []fv1.HTTPTrigger
	functions                  []fv1.Function
	updateRouterRequestChannel chan struct{}
	tsRoundTripperParams       *tsRoundTripperParams
	isDebugEnv                 bool
	structuredErrors           bool
	accessLog                  bool
	useEncodedPath             bool
	syncDebouncer              func(func())
	// ready flips true after the first successful mux build; routerReadinessHandler
	// gates /readyz on it so a starting/rolling pod stays out of the
	// Service endpoints until its mux is populated.
	ready atomic.Bool

	// Incremental route updates (RFC-0013) are the only production path: the
	// reconcilers feed per-event diffs into routeTable and the materializer
	// rebuilds muxes only on shape changes. initIncrementalRoutes wires the
	// table before the Manager starts.
	routeTable *routetable.Table
	// pendingConditions holds triggers whose shape change is waiting on the
	// debounced materialize; their RouteAdmitted conditions are marked after
	// the swap so conditions reflect observable state. conflictLosers tracks
	// the triggers currently shadowed by a route conflict (phase 2): written
	// by materialize, read by the apply path (reconcilers + resync) through
	// isConflictLoser, so both maps share pendingMu.
	pendingMu         sync.Mutex
	pendingConditions map[types.UID]*fv1.HTTPTrigger
	conflictLosers    map[types.NamespacedName]routetable.Conflict
	// materializeDirty is set when a materialize attempt fails, so the
	// resync loop re-signals until a build succeeds — a consumed signal must
	// not strand table state out of the served mux.
	materializeDirty atomic.Bool
	// featureConfigFn is the materializer's feature-config source —
	// injectable so tests can exercise the materialize failure path.
	featureConfigFn func(logr.Logger) (*config.FeatureConfig, error)

	// asyncInvoker enqueues RFC-0024 async invocations. Set by Start after
	// makeHTTPTriggerSet (like addressResolver) when ASYNC_INVOCATION_ENABLED, and
	// copied into public function handlers by buildTriggerHandler. nil = feature off.
	asyncInvoker *asyncInvoker
}

// initIncrementalRoutes wires the route table and feature-config source for the
// incremental route path (the only production path). Must be called before
// subscribeRouter / the Manager starts.
func (ts *HTTPTriggerSet) initIncrementalRoutes() {
	ts.routeTable = routetable.New()
	ts.featureConfigFn = config.GetFeatureConfig
}

// makeHTTPTriggerSet builds the trigger set. cl is the Manager's cache-backed
// client (mgr.GetClient()); it backs both the trigger/function reconcilers and
// the resolver. A nil cl is the unit-test path that drives buildMuxes directly
// without a Manager.
func makeHTTPTriggerSet(logger logr.Logger, fmap *functionServiceMap, fissionClient versioned.Interface,
	kubeClient kubernetes.Interface, cl client.Client, executor eclient.ClientInterface, params *tsRoundTripperParams, isDebugEnv bool, useEncodedPath bool, unTapServiceTimeout time.Duration, actionThrottler *throttler.Throttler) (*HTTPTriggerSet, error) {

	httpTriggerSet := &HTTPTriggerSet{
		logger:                     logger.WithName("http_trigger_set"),
		triggers:                   []fv1.HTTPTrigger{},
		fissionClient:              fissionClient,
		kubeClient:                 kubeClient,
		client:                     cl,
		updateRouterRequestChannel: make(chan struct{}, 10), // use buffer channel
		tsRoundTripperParams:       params,
		isDebugEnv:                 isDebugEnv,
		useEncodedPath:             useEncodedPath,
		syncDebouncer:              debounce.New(time.Millisecond * 20),
	}
	httpTriggerSet.resolver = makeFunctionReferenceResolver(logger, cl)
	// The address resolver and tapper are the proxy path's injected seams
	// (RFC-0002). Start (router.go) swaps addressResolver for the slice-fed
	// fallback resolver before the first mux build when the cache mode is on.
	httpTriggerSet.addressResolver = &executorResolver{
		logger:    logger.WithName("executor_resolver"),
		fmap:      fmap,
		reader:    cl,
		executor:  executor,
		throttler: actionThrottler,
	}
	httpTriggerSet.tapper = &executorTapper{
		logger:       logger.WithName("tapper"),
		executor:     executor,
		unTapTimeout: unTapServiceTimeout,
	}
	return httpTriggerSet, nil
}

// subscribeRouter wires the public mutable router and (optionally) the
// internal mutable router. Passing internalMR=nil preserves the legacy
// single-listener path used by older test scaffolding.
func (ts *HTTPTriggerSet) subscribeRouter(ctx context.Context, mgr *errgroup.Group, mr *mutableRouter, internalMR *mutableRouter) error {
	ts.mutableRouter = mr
	ts.internalMutableRouter = internalMR

	if ts.fissionClient == nil {
		// Used in tests only.
		public, internal, err := ts.buildMuxes(ctx, nil)
		if err != nil {
			return err
		}
		mr.updateRouter(public.Handler())
		if internalMR != nil {
			internalMR.updateRouter(internal.Handler())
		}
		ts.logger.Info("skipping continuous trigger updates")
		return nil
	}
	// The trigger/function reconcilers (registered on the Manager) signal mux
	// rebuilds through updateRouterRequestChannel; this goroutine consumes them.
	// The Manager owns the informer caches, so there are no informers to Run
	// here. The initial mux build is kicked off by Start once the cache syncs.
	// The materializer (RFC-0013) consumes the channel and rebuilds the muxes
	// from a route-table snapshot on shape changes.
	mgr.Go(func() error {
		ts.materializeLoop(ctx)
		return nil
	})
	return nil
}

func defaultHomeHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func routerHealthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// routerReadinessHandler reports 200 only once the first mux build has
// succeeded (which happens after the Manager's trigger + function caches sync).
// The kubelet keeps a freshly started or rolling router pod out of the Service
// endpoints until its mux is populated, avoiding 404s for valid triggers during
// startup and rolling updates. /router-healthz stays a cheap liveness check.
func (ts *HTTPTriggerSet) routerReadinessHandler(w http.ResponseWriter, r *http.Request) {
	// ready flips true after the first successful mux build, which only happens
	// once the Manager's trigger + function caches have synced. Until then the
	// mux has no user routes, so report 503 to stay out of the Service endpoints.
	if !ts.ready.Load() {
		http.Error(w, "router mux not yet built", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// panicRecoveryMiddleware keeps a single panicking request (e.g. a write
// failure deep inside the reverse proxy) from crashing the whole router
// process and dropping every other in-flight request. It is installed inside
// newListenerMuxes (used by both buildMuxes and the incremental
// buildIncrementalMuxes) so it survives the atomic mux swap and applies to both
// the public and internal listeners.
func panicRecoveryMiddleware(logger logr.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// net/http uses ErrAbortHandler as a sentinel for an
				// intentional abort; re-panic so its own recover handles it.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logger.Error(nil, "recovered from panic in handler",
					"panic", fmt.Sprintf("%v", rec),
					"path", r.URL.Path, "method", r.Method)
				// Best effort: sets 502 if nothing was written yet. If the
				// response is already underway (e.g. a hijacked/streamed
				// connection) net/http ignores this with a logged warning.
				w.WriteHeader(http.StatusBadGateway)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write([]byte(info.ApiInfo().String()))
	if err != nil {
		se, ok := err.(*kerrors.StatusError)
		if ok {
			http.Error(w, se.Error(), int(se.ErrStatus.Code))
			return
		}

		code, msg := ferror.GetHTTPError(err)
		http.Error(w, msg, code)
	}
}

// buildMuxes is the one-shot mux constructor: it builds both listener muxes
// from the current trigger + function snapshot in a single pass. Production
// route updates go through the incremental materializer (incremental.go);
// buildMuxes is the test/parity builder and the test-scaffolding path in
// subscribeRouter (fissionClient == nil), sharing the registration helpers in
// routeshape.go so it stays byte-identical with the materializer.
//
// It constructs the two mux.Router instances that back the router
// process: one for the public listener (user HTTPTriggers + healthz +
// version + optional auth) and one for the internal listener
// (`/fission-function/<ns>/<name>` and its prefix variant). Splitting
// the registrations is the core of GHSA-3g33-6vg6-27m8 — internal
// invocation routes must never be reachable from the public listener,
// because they bypass HTTPTrigger gates (auth middleware, host/method
// matching) by design.
//
// The internal mux deliberately omits the metrics middleware, the auth
// middleware, and the GKE-ingress `/` no-op handler: those concerns are
// public-listener only. Callers wrap the internal mux with
// hmac.Verifier in the bundle process; the verifier is intentionally
// not added here so this function stays unit-testable without HMAC env
// state.
// precomputePolicies resolves the proxy policy for every backend function of
// a route at mux-build time (RFC-0014): resolveProxyPolicy is a pure function
// of (fn, timeout, idle-default), and the canary path selects the backend per
// request, so the map is keyed by function UID.
func precomputePolicies(fns map[string]*fv1.Function, fnTimeoutMap map[types.UID]int, streamIdleDefault time.Duration) map[types.UID]proxyPolicy {
	policies := make(map[types.UID]proxyPolicy, len(fns))
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		fnTimeout := fnTimeoutMap[fn.GetUID()]
		if fnTimeout == 0 {
			fnTimeout = fv1.DEFAULT_FUNCTION_TIMEOUT
		}
		policies[fn.GetUID()] = resolveProxyPolicy(fn, time.Duration(fnTimeout)*time.Second, streamIdleDefault)
	}
	return policies
}

func (ts *HTTPTriggerSet) buildMuxes(ctx context.Context, fnTimeoutMap map[types.UID]int) (public, internal *httpmux.Mux, err error) {
	featureConfig, err := config.GetFeatureConfig(ts.logger)
	if err != nil {
		return nil, nil, err
	}

	publicMux, internalMux := ts.newListenerMuxes(featureConfig)

	// HTTP triggers setup by the user — public listener only.
	homeHandled := false
	for i := range ts.triggers {
		trigger := ts.triggers[i]

		// Skip triggers whose CORS / ingress config is invalid. Registering a
		// route for one would apply broken CORS or a malformed ingress path;
		// the RouteAdmitted=False condition (set by the incremental apply path
		// in incremental.go) tells the user why the route is not served. The
		// httptrigger admission webhook used to reject these.
		if _, err := triggerConfigError(&trigger); err != nil {
			continue
		}

		// resolve function reference
		rr, err := ts.resolver.resolve(ctx, trigger)
		if err != nil {
			// Unresolvable function reference: skip the route and let it 404.
			// The incremental apply path (incremental.go) reports it on the
			// trigger's conditions.
			ts.logger.Error(err, "skipping trigger with unresolvable function reference",
				"trigger", trigger.Name, "namespace", trigger.Namespace)
			continue
		}

		if rr.resolveResultType != resolveResultSingleFunction && rr.resolveResultType != resolveResultMultipleFunctions {
			// Unsupported resolve result type. Skip the route (let it 404)
			// instead of crashing the whole router process and dropping every
			// other trigger with it.
			ts.logger.Error(nil, "resolve result type not implemented", "type", rr.resolveResultType)
			continue
		}

		shape := deriveRouteShape(&trigger)
		handler := ts.buildTriggerHandler(&trigger, rr, fnTimeoutMap)
		registerRouteShape(publicMux, shape, handler)
		ts.logger.V(1).Info("registered trigger route",
			"trigger", trigger.Name, "exact", shape.exactPath, "prefix", shape.prefixPath, "methods", shape.methods)

		if shape.claimsHome() {
			homeHandled = true
		}
	}

	// Internal routes for each function by name. Non-http triggers
	// (timer, kubewatcher, mqtrigger) and the executor's invocation
	// path land here. These routes live ONLY on the internal mux —
	// exposing them on the public listener is GHSA-3g33-6vg6-27m8.
	for i := range ts.functions {
		fn := ts.functions[i]
		handler := ts.buildInternalFunctionHandler(&fn, fnTimeoutMap)
		key := types.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}
		registerInternalRoute(internalMux, key, handler)
		ts.logger.V(1).Info("add internal handler and prefix route for function", "function", fn.Name)
	}

	ts.registerRouterOwnedRoutes(publicMux, featureConfig, homeHandled)
	ts.registerAsyncDLQRoutes(internalMux)

	return publicMux, internalMux, nil
}

// toAllowlistConfig converts an HTTPTriggerCorsConfig (the user-facing
// CRD field) into an httpsecurity.AllowlistConfig (the middleware-facing
// struct). When the trigger does not set AllowMethods, the trigger's
// HTTP methods fall in so a preflight against the trigger's allowed
// methods succeeds without the user having to duplicate the list.
//
// MaxAge has already been validated at admission (see
// HTTPTriggerCorsConfig.Validate); any parse error here would indicate
// a regression in validation, so the helper falls back to zero rather
// than failing the reconcile.
func toAllowlistConfig(cfg *fv1.HTTPTriggerCorsConfig, triggerMethods []string) httpsecurity.AllowlistConfig {
	methods := cfg.AllowMethods
	if len(methods) == 0 {
		methods = triggerMethods
	}
	var maxAge time.Duration
	if cfg.MaxAge != "" {
		if d, err := time.ParseDuration(cfg.MaxAge); err == nil {
			maxAge = d
		}
	}
	return httpsecurity.AllowlistConfig{
		AllowOrigins:     cfg.AllowOrigins,
		AllowMethods:     methods,
		AllowHeaders:     cfg.AllowHeaders,
		ExposeHeaders:    cfg.ExposeHeaders,
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           maxAge,
	}
}

// triggerConfigError reports whether an HTTPTrigger's CORS or ingress
// configuration is invalid, returning the matching RouteAdmitted Reason and the
// error, or ("", nil) when the config is valid. These checks rely on Go parsers
// (url.Parse, time.ParseDuration, regexp.CompilePOSIX) that CRD CEL cannot
// express; with the httptrigger admission webhook removed, the router validates
// here and surfaces the result as the trigger's RouteAdmitted condition instead
// of rejecting at admission. (CorsConfig.Validate is nil-safe.)
func triggerConfigError(trigger *fv1.HTTPTrigger) (reason string, err error) {
	if e := trigger.Spec.CorsConfig.Validate(); e != nil {
		return fv1.HTTPTriggerReasonInvalidCorsConfig, e
	}
	if e := trigger.Spec.IngressConfig.Validate(); e != nil {
		return fv1.HTTPTriggerReasonInvalidIngressConfig, e
	}
	// httpmux template compile check: a malformed template (unbalanced braces,
	// empty var name, or an uncompilable regexp class) would register a
	// silently-dead route — and would panic httpmux.Handler() at build time if
	// it reached the mux. CompilePattern returns the error instead, and CEL
	// cannot express this, so the router is the gate. (Unlike gorilla, capturing
	// groups such as {sort:(asc|desc)} are valid here and admit.)
	if e := validateRouteTemplate(deriveRouteShape(trigger)); e != nil {
		return fv1.HTTPTriggerReasonInvalidRouteTemplate, e
	}
	return "", nil
}

// markTriggerCondition writes RouteAdmitted + Ready conditions on an
// HTTPTrigger. status=True with reason=RouteAdmitted on a successful mux
// install; status=False with reason=MuxBuildFailed when buildMuxes errors
// out, so users polling conditions don't see a stale True after a failure.
//
// Fast-path: the trigger snapshot from the informer is checked against
// the desired condition; we only Get + UpdateStatus when this trigger
// would actually transition (matches the user's "key transitions only"
// expectation). The incremental apply path runs on every reconciled
// trigger/function event, so the fast path is what keeps API traffic bounded.
func (ts *HTTPTriggerSet) markTriggerCondition(ctx context.Context, trigger *fv1.HTTPTrigger, status metav1.ConditionStatus, reason, admittedMessage, readyMessage string) {
	if ts.fissionClient == nil {
		return // unit-test wiring without a real client
	}
	// Truncate to the apiserver's 32KB Condition.message cap; the failure
	// path embeds err.Error() which is otherwise unbounded.
	admittedMessage = conditions.TruncateMessage(admittedMessage)
	readyMessage = conditions.TruncateMessage(readyMessage)
	wantAdmitted := metav1.Condition{
		Type: fv1.HTTPTriggerConditionRouteAdmitted, Status: status,
		ObservedGeneration: trigger.Generation, Reason: reason, Message: admittedMessage,
	}
	wantReady := metav1.Condition{
		Type: fv1.HTTPTriggerConditionReady, Status: status,
		ObservedGeneration: trigger.Generation, Reason: reason, Message: readyMessage,
	}
	if conditions.IsAt(trigger.Status.Conditions, wantAdmitted) &&
		conditions.IsAt(trigger.Status.Conditions, wantReady) {
		return
	}
	cur, err := ts.fissionClient.CoreV1().HTTPTriggers(trigger.Namespace).Get(ctx, trigger.Name, metav1.GetOptions{})
	if err != nil {
		ts.logger.V(1).Info("httptrigger status: get failed", "name", trigger.Name, "namespace", trigger.Namespace, "error", err)
		return
	}
	wantAdmitted.ObservedGeneration = cur.Generation
	wantReady.ObservedGeneration = cur.Generation
	if conditions.IsAt(cur.Status.Conditions, wantAdmitted) &&
		conditions.IsAt(cur.Status.Conditions, wantReady) {
		return
	}
	conditions.Set(&cur.Status.Conditions, wantAdmitted)
	conditions.Set(&cur.Status.Conditions, wantReady)
	if _, err := ts.fissionClient.CoreV1().HTTPTriggers(trigger.Namespace).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		ts.logger.V(1).Info("httptrigger status: update failed", "name", trigger.Name, "namespace", trigger.Namespace, "error", err)
	}
}
