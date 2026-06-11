// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bep/debounce"
	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
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
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/metrics"
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
	// reconcilers and updateRouter read HTTPTriggers + Functions through it,
	// replacing the per-namespace SharedIndexInformers the router used before
	// the controller-runtime migration.
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
	useEncodedPath             bool
	syncDebouncer              func(func())
	// ready flips true after the first successful mux build; routerReadinessHandler
	// gates /readyz on it so a starting/rolling pod stays out of the
	// Service endpoints until its mux is populated.
	ready atomic.Bool
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
		mr.updateRouter(public)
		if internalMR != nil {
			internalMR.updateRouter(internal)
		}
		ts.logger.Info("skipping continuous trigger updates")
		return nil
	}
	// The trigger/function reconcilers (registered on the Manager) signal mux
	// rebuilds through updateRouterRequestChannel; this goroutine consumes them.
	// The Manager owns the informer caches, so there are no informers to Run
	// here. The initial mux build is kicked off by Start once the cache syncs.
	mgr.Go(func() error {
		ts.updateRouter(ctx)
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
// buildMuxes so it survives the atomic mux swap and applies to both the public
// and internal listeners.
func panicRecoveryMiddleware(logger logr.Logger) mux.MiddlewareFunc {
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

// buildMuxes constructs the two mux.Router instances that back the router
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
func (ts *HTTPTriggerSet) buildMuxes(ctx context.Context, fnTimeoutMap map[types.UID]int) (public, internal *mux.Router, err error) {
	featureConfig, err := config.GetFeatureConfig(ts.logger)
	if err != nil {
		return nil, nil, err
	}

	public = mux.NewRouter()
	internal = mux.NewRouter()
	// Honour USE_ENCODED_PATH (see issue #1317) on every reconciliation:
	// buildMuxes is called repeatedly by updateRouter and the resulting
	// routers are atomically swapped under the listener handlers. If we
	// don't apply UseEncodedPath here, the feature works only until the
	// first reconciliation and then silently turns off.
	if ts.useEncodedPath {
		public = public.UseEncodedPath()
		internal = internal.UseEncodedPath()
	}

	// Panic recovery is the outermost middleware so it also catches panics in
	// the metrics/auth middleware below. Applied to both listeners.
	public.Use(panicRecoveryMiddleware(ts.logger))
	internal.Use(panicRecoveryMiddleware(ts.logger))

	public.Use(metrics.HTTPMetricMiddleware)
	if featureConfig.AuthConfig.IsEnabled {
		public.Use(authMiddleware(featureConfig))
	}

	// HTTP triggers setup by the user — public listener only.
	homeHandled := false
	for i := range ts.triggers {
		trigger := ts.triggers[i]

		// Skip triggers whose CORS / ingress config is invalid. Registering a
		// route for one would apply broken CORS or a malformed ingress path;
		// the RouteAdmitted=False condition (set by the post-build loop in
		// updateRouter) tells the user why the route is not served. The
		// httptrigger admission webhook used to reject these.
		if _, err := triggerConfigError(&trigger); err != nil {
			continue
		}

		// resolve function reference
		rr, err := ts.resolver.resolve(ctx, trigger)
		if err != nil {
			// Unresolvable function reference. Report the error via
			// the trigger's status.
			go ts.updateTriggerStatusFailed(&trigger, err)

			// Ignore this route and let it 404.
			continue
		}

		if rr.resolveResultType != resolveResultSingleFunction && rr.resolveResultType != resolveResultMultipleFunctions {
			// Unsupported resolve result type. Report it via the trigger's
			// status and skip the route (let it 404) instead of crashing the
			// whole router process and dropping every other trigger with it.
			ts.logger.Error(nil, "resolve result type not implemented", "type", rr.resolveResultType)
			go ts.updateTriggerStatusFailed(&trigger, fmt.Errorf("resolve result type not implemented: %v", rr.resolveResultType))
			continue
		}

		fh := &functionHandler{
			logger:                   ts.logger.WithName(trigger.Name),
			resolver:                 ts.addressResolver,
			tapper:                   ts.tapper,
			httpTrigger:              &trigger,
			functionMap:              rr.functionMap,
			fnWeightDistributionList: rr.functionWtDistributionList,
			tsRoundTripperParams:     ts.tsRoundTripperParams,
			isDebugEnv:               ts.isDebugEnv,
			functionTimeoutMap:       fnTimeoutMap,
		}

		// The functionHandler for HTTP trigger with fn reference type "FunctionReferenceTypeFunctionName",
		// it's function metadata is set here.

		// The functionHandler For HTTP trigger with fn reference type "FunctionReferenceTypeFunctionWeights",
		// it's function metadata is decided dynamically before proxying the request in order to support canary
		// deployment. For more details, please check "handler" function of functionHandler.

		if rr.resolveResultType == resolveResultSingleFunction {
			for _, fn := range fh.functionMap {
				fh.function = fn
			}
		}

		methods := trigger.Spec.Methods
		if len(trigger.Spec.Method) > 0 {
			present := slices.Contains(trigger.Spec.Methods, trigger.Spec.Method)
			if !present {
				methods = append(methods, trigger.Spec.Method)
			}
		}

		// Per-trigger CORS: if the trigger declares a CorsConfig the
		// router applies a CORSAllowlist middleware around its handler
		// and appends OPTIONS to the registered methods so gorilla/mux
		// routes the preflight to the wrapped handler instead of
		// returning 405 before CORSAllowlist sees the request.
		// Triggers without a CorsConfig keep the deny-by-default
		// behaviour — no Access-Control-* headers, SOP blocks cross-
		// origin reads.
		var handler http.Handler = http.HandlerFunc(fh.handler)
		if trigger.Spec.CorsConfig != nil {
			cfg := toAllowlistConfig(trigger.Spec.CorsConfig, methods)
			handler = httpsecurity.CORSAllowlist(cfg)(handler)
			if !slices.Contains(methods, http.MethodOptions) {
				methods = append(methods, http.MethodOptions)
			}
		}

		if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
			prefix := *trigger.Spec.Prefix
			if strings.HasSuffix(prefix, "/") {
				ht := public.PathPrefix(prefix).Handler(handler)
				ht.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht.Host(trigger.Spec.Host)
				}
				ts.logger.V(1).Info("add prefix route for function", "route", prefix, "function", fh.function, "methods", methods)
			} else {
				ht1 := public.Handle(prefix, handler)
				ht1.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht1.Host(trigger.Spec.Host)
				}
				ht2 := public.PathPrefix(prefix + "/").Handler(handler)
				ht2.Methods(methods...)
				if trigger.Spec.Host != "" {
					ht2.Host(trigger.Spec.Host)
				}
				ts.logger.V(1).Info("add prefix and handler route for function", "route", prefix, "function", fh.function, "methods", methods)
			}
		} else {
			ht := public.Handle(trigger.Spec.RelativeURL, handler)
			ht.Methods(methods...)
			if trigger.Spec.Host != "" {
				ht.Host(trigger.Spec.Host)
			}
			ts.logger.V(1).Info("add handler route for function", "router", trigger.Spec.RelativeURL, "function", fh.function, "methods", methods)
		}

		if trigger.Spec.Prefix == nil && trigger.Spec.RelativeURL == "/" && len(methods) == 1 && methods[0] == http.MethodGet {
			homeHandled = true
		}
	}
	if !homeHandled {
		//
		// This adds a no-op handler that returns 200-OK to make sure that the
		// "GET /" request succeeds.  This route is used by GKE Ingress (and
		// perhaps other ingress implementations) as a health check, so we don't
		// want it to be a 404 even if the user doesn't have a function mapped to
		// this route.
		//
		// Router-owned probe; never a CORS surface for legitimate
		// browser code, so reject cross-origin preflights and strip any
		// Access-Control-* header that might be added in the future.
		// OPTIONS is registered alongside GET so a preflight reaches
		// DenyAllCORS instead of being 405'd by mux's method gate.
		public.Handle("/", httpsecurity.DenyAllCORS(http.HandlerFunc(defaultHomeHandler))).Methods(http.MethodGet, http.MethodOptions)
	}

	// Internal triggers for each function by name. Non-http triggers
	// (timer, kubewatcher, mqtrigger) and the executor's invocation
	// path land here. These routes live ONLY on the internal mux —
	// exposing them on the public listener is GHSA-3g33-6vg6-27m8.
	for i := range ts.functions {
		fn := ts.functions[i]
		fh := &functionHandler{
			logger:               ts.logger.WithName(fn.Name),
			resolver:             ts.addressResolver,
			tapper:               ts.tapper,
			function:             &fn,
			tsRoundTripperParams: ts.tsRoundTripperParams,
			isDebugEnv:           ts.isDebugEnv,
			functionTimeoutMap:   fnTimeoutMap,
		}

		internalRoute := utils.UrlForFunction(fn.Name, fn.Namespace)
		internalPrefixRoute := internalRoute + "/"
		handler := http.HandlerFunc(fh.handler)
		internal.Handle(internalRoute, handler)
		internal.PathPrefix(internalPrefixRoute).Handler(handler)
		ts.logger.V(1).Info("add internal handler and prefix route for function", "router", internalRoute, "function", fn)
	}

	if featureConfig.AuthConfig.IsEnabled {

		path := featureConfig.AuthConfig.AuthUriPath
		// Auth endpoint for the router. Router-owned route; cross-origin
		// browser callers are not a legitimate use case, so reject
		// preflights and strip any stray Access-Control-* headers.
		// OPTIONS registered so the preflight reaches DenyAllCORS.
		public.Handle(path, httpsecurity.DenyAllCORS(http.HandlerFunc(authLoginHandler(featureConfig)))).Methods(http.MethodPost, http.MethodOptions)
	}

	// Healthz endpoint for the router. Stays on the public listener so
	// existing readiness/liveness probes and external monitors keep
	// working without HMAC credentials. Router-owned route; deny CORS
	// preflights (OPTIONS registered so mux routes them to DenyAllCORS).
	public.Handle("/router-healthz", httpsecurity.DenyAllCORS(http.HandlerFunc(routerHealthHandler))).Methods(http.MethodGet, http.MethodOptions)
	// Readiness probe: 200 only once informer caches have synced. Public
	// listener, next to /router-healthz; no HMAC needed. Router-owned route;
	// deny CORS (OPTIONS registered so mux routes preflights to DenyAllCORS).
	public.Handle("/readyz", httpsecurity.DenyAllCORS(http.HandlerFunc(ts.routerReadinessHandler))).Methods(http.MethodGet, http.MethodOptions)
	// version of application; router-owned route; deny CORS.
	public.Handle("/_version", httpsecurity.DenyAllCORS(http.HandlerFunc(versionHandler))).Methods(http.MethodGet, http.MethodOptions)

	return public, internal, nil
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
	return "", nil
}

func (ts *HTTPTriggerSet) updateTriggerStatusFailed(ht *fv1.HTTPTrigger, err error) {
	// TODO
}

// markTriggerCondition writes RouteAdmitted + Ready conditions on an
// HTTPTrigger. status=True with reason=RouteAdmitted on a successful mux
// install; status=False with reason=MuxBuildFailed when buildMuxes errors
// out, so users polling conditions don't see a stale True after a failure.
//
// Fast-path: the trigger snapshot from the informer is checked against
// the desired condition; we only Get + UpdateStatus when this trigger
// would actually transition (matches the user's "key transitions only"
// expectation). updateRouter runs on every debounced trigger/function
// event, so the fast path is what keeps API traffic bounded.
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

func (ts *HTTPTriggerSet) syncTriggers() {
	ts.syncDebouncer(func() {
		ts.updateRouterRequestChannel <- struct{}{}
	})
}

func (ts *HTTPTriggerSet) updateRouter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ts.updateRouterRequestChannel:
		}
		// get triggers + functions from the Manager's cache (scoped to the
		// Fission-watched namespaces via FissionCacheOptions).
		var triggerList fv1.HTTPTriggerList
		if err := ts.client.List(ctx, &triggerList); err != nil {
			ts.logger.Error(err, "error listing http triggers; skipping mux rebuild")
			continue
		}
		ts.triggers = triggerList.Items

		var functionList fv1.FunctionList
		if err := ts.client.List(ctx, &functionList); err != nil {
			ts.logger.Error(err, "error listing functions; skipping mux rebuild")
			continue
		}
		allfunctions := functionList.Items
		functionTimeout := make(map[types.UID]int, len(allfunctions))
		for i := range allfunctions {
			functionTimeout[allfunctions[i].UID] = allfunctions[i].Spec.FunctionTimeout
		}
		ts.functions = allfunctions
		alltriggers := ts.triggers

		// Rebuild both muxes from the same function snapshot then swap
		// each in turn. The two updateRouter calls are sequential, not
		// transactional — a request that arrives between the two swaps
		// could see an updated public mux and a stale internal mux for
		// a few microseconds, but both muxes derive from the same
		// `functions` snapshot so the function set served by either
		// listener is consistent in steady state. True atomicity across
		// listeners would require a shared atomic.Pointer holding both
		// muxes; not worth the complexity for this case.
		public, internal, err := ts.buildMuxes(ctx, functionTimeout)
		if err != nil {
			ts.logger.Error(err, "error updating router")
			// Flip every trigger to Ready=False so consumers polling
			// conditions don't see a stale True after a failed resync.
			// Fast-path inside markTriggerCondition skips no-op writes.
			for i := range alltriggers {
				ts.markTriggerCondition(ctx, &alltriggers[i],
					metav1.ConditionFalse, fv1.HTTPTriggerReasonMuxBuildFail,
					"router failed to build mux: "+err.Error(),
					"trigger is not serving due to router mux error")
			}
			continue
		}
		ts.mutableRouter.updateRouter(public)
		if ts.internalMutableRouter != nil {
			ts.internalMutableRouter.updateRouter(internal)
		}
		// The mux now serves user routes — report ready (gates /readyz).
		ts.ready.Store(true)

		// Mark each trigger admitted now that its routes are live. We
		// do this after the swap so the condition reflects observable
		// state, not just intent. Best-effort; never blocks subsequent
		// mux updates.
		for i := range alltriggers {
			// Triggers with invalid CORS/ingress config were skipped in
			// buildMuxes; report that as RouteAdmitted=False (the Go-parser
			// checks CEL can't express) instead of a stale True.
			if reason, cfgErr := triggerConfigError(&alltriggers[i]); cfgErr != nil {
				ts.markTriggerCondition(ctx, &alltriggers[i],
					metav1.ConditionFalse, reason,
					"router rejected the trigger configuration: "+cfgErr.Error(),
					"trigger is not serving due to invalid configuration")
				continue
			}
			ts.markTriggerCondition(ctx, &alltriggers[i],
				metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted,
				"router accepted the trigger and installed its mux entry",
				"trigger is serving")
		}
	}
}
