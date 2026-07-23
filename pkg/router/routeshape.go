// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/metrics"
)

// This file holds the route-shape derivation and mux-registration helpers
// shared by the incremental materializer (RFC-0013, the only production
// route-update path) and the one-shot buildMuxes constructor (the
// test/parity builder). Keeping them shared is what lets the golden shape
// tests guarantee both builders register identical routes.

// routeShape is the mux-visible part of a trigger: what buildMuxes registers
// and what the route table treats as "shape" (changes rebuild the mux) as
// opposed to "handler" (changes swap a pointer).
type routeShape struct {
	// exactPath / prefixPath mirror the three registration forms:
	// RelativeURL → exact only; slash-suffixed Prefix → prefix only;
	// non-slash Prefix → BOTH (the dual-registration pair that keeps
	// /api from matching /apifoo).
	exactPath  string
	prefixPath string
	host       string
	methods    []string
}

// triggerMethods merges the trigger's method set with the deprecated
// singular Method field (pre-CORS-append form — the CORS allowlist fallback
// uses this list, not the OPTIONS-augmented one).
func triggerMethods(trigger *fv1.HTTPTrigger) []string {
	methods := trigger.Spec.Methods
	if len(trigger.Spec.Method) > 0 && !slices.Contains(methods, trigger.Spec.Method) {
		// Copy-on-append so the merge never aliases the trigger object's
		// slice (the informer cache owns it).
		methods = append(slices.Clone(methods), trigger.Spec.Method)
	}
	return methods
}

// deriveRouteShape computes the registrations a trigger produces: the
// exact/prefix path forms and the final method set (with OPTIONS appended
// for CORS triggers so the preflight reaches the CORSAllowlist wrapper
// instead of the mux's 405).
func deriveRouteShape(trigger *fv1.HTTPTrigger) routeShape {
	shape := routeShape{
		host:    trigger.Spec.Host,
		methods: triggerMethods(trigger),
	}
	if trigger.Spec.CorsConfig != nil && !slices.Contains(shape.methods, http.MethodOptions) {
		shape.methods = append(slices.Clone(shape.methods), http.MethodOptions)
	}
	if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
		prefix := *trigger.Spec.Prefix
		if prefix[len(prefix)-1] == '/' {
			shape.prefixPath = prefix
		} else {
			shape.exactPath = prefix
			shape.prefixPath = prefix + "/"
		}
	} else {
		shape.exactPath = trigger.Spec.RelativeURL
	}
	return shape
}

// claimsHome reports whether this shape claims "GET /" exactly, which
// suppresses the router-owned GKE-ingress health fallback on "/".
func (s routeShape) claimsHome() bool {
	return s.prefixPath == "" && s.exactPath == "/" &&
		len(s.methods) == 1 && s.methods[0] == http.MethodGet
}

// registerRouteShape registers a shape onto a mux with the given handler:
// up to two httpmux routes (exact and/or prefix), each gated by the shape's
// methods and optional host.
//
// The method slice is cloned per registration. httpmux's Route.Methods does
// not mutate its argument (it compares case-insensitively at match time), but
// it RETAINS the slice as the route's matcher — passing the shape's slice
// directly would keep a live reference into the route table's canonical spec
// or the informer-owned trigger object, which a concurrent reconcile could
// mutate under the still-serving previous mux. The clone severs that.
//
// .Methods is called UNCONDITIONALLY (even for an empty shape.methods): in
// httpmux an empty method set is a DEAD route (matches nothing), which is the
// derived empty-method shape the router must preserve. Skipping the call for an
// empty set would silently widen the route to match every method — see
// TestRouteShapeEmptyMethods / httpmux TestEmptyMethodsMatchesNothing.
func registerRouteShape(m *httpmux.Mux, shape routeShape, handler http.Handler) {
	if shape.exactPath != "" {
		route := m.Handle(shape.exactPath, handler).Methods(slices.Clone(shape.methods)...)
		if shape.host != "" {
			route.Host(shape.host)
		}
	}
	if shape.prefixPath != "" {
		route := m.HandlePrefix(shape.prefixPath, handler).Methods(slices.Clone(shape.methods)...)
		if shape.host != "" {
			route.Host(shape.host)
		}
	}
}

// internalRoutePair returns the internal listener's route pair for a
// function: the exact /fission-function/... URL and its slash subtree.
// utils.UrlForFunction folds the default namespace — the form every internal
// publisher builds.
func internalRoutePair(key types.NamespacedName) (exact, prefix string) {
	exact = utils.UrlForFunction(key.Name, key.Namespace)
	return exact, exact + "/"
}

// registerInternalRoute registers a function's internal-listener route pair —
// the exact /fission-function/... URL plus its slash subtree — onto the
// internal mux. No method gate (the HMAC verifier the bundle wraps is the
// access control). Shared by the one-shot buildMuxes and the incremental
// buildIncrementalMuxes so the two builders register the internal routes
// identically, mirroring registerRouteShape on the public side.
func registerInternalRoute(m *httpmux.Mux, key types.NamespacedName, handler http.Handler) {
	exact, prefix := internalRoutePair(key)
	m.Handle(exact, handler)
	m.HandlePrefix(prefix, handler)
}

// validateRouteTemplate reports whether a shape's path templates compile.
// Templates reach the router unvalidated: there is no HTTPTrigger admission
// webhook and CEL cannot run the template parser, so a malformed template
// (unbalanced braces, empty var name, an uncompilable regexp class) would
// register a route that silently never matches. httpmux.CompilePattern returns
// the compile error rather than panicking, and both builders (the one-shot
// buildMuxes and the incremental materialize) reject the trigger through
// triggerConfigError, surfacing RouteAdmitted=False/InvalidRouteTemplate.
//
// Unlike gorilla, httpmux accepts capturing groups such as {sort:(asc|desc)}
// (it wraps each variable in a NAMED group), so that previously-rejected but
// reasonable pattern now admits instead of erroring.
func validateRouteTemplate(shape routeShape) error {
	if shape.exactPath != "" {
		if err := httpmux.CompilePattern(shape.exactPath, httpmux.Exact); err != nil {
			return err
		}
	}
	if shape.prefixPath != "" {
		if err := httpmux.CompilePattern(shape.prefixPath, httpmux.Prefix); err != nil {
			return err
		}
	}
	return nil
}

// buildTriggerHandler constructs the proxy handler for one trigger from its
// resolve result: the functionHandler with hoisted per-route state
// (RFC-0014) plus the per-trigger CORS wrap. fnTimeoutMap may be the global
// map (one-shot buildMuxes) or a per-trigger map derived from the resolved
// functions (incremental path) — the handler only ever looks up its own backends.
func (ts *HTTPTriggerSet) buildTriggerHandler(trigger *fv1.HTTPTrigger, rr *resolveResult, fnTimeoutMap map[crd.CacheKeyUG]int) http.Handler {
	var streamIdleDefault time.Duration
	if ts.tsRoundTripperParams != nil {
		streamIdleDefault = ts.tsRoundTripperParams.streamIdleDefault
	}
	routeLogger := ts.logger.WithName(trigger.Name)
	fh := &functionHandler{
		logger:                   routeLogger,
		resolver:                 ts.addressResolver,
		tapper:                   ts.tapper,
		httpTrigger:              trigger,
		functionMap:              rr.functionMap,
		fnWeightDistributionList: rr.functionWtDistributionList,
		tsRoundTripperParams:     ts.tsRoundTripperParams,
		isDebugEnv:               ts.isDebugEnv,
		structuredErrors:         ts.structuredErrors,
		accessLog:                ts.accessLog,
		functionTimeoutMap:       fnTimeoutMap,
		rtLogger:                 routeLogger.WithName("roundtripper"),
		policyByUID:              precomputePolicies(rr.functionMap, fnTimeoutMap, streamIdleDefault),
		asyncInvoker:             ts.asyncInvoker,
	}

	// For FunctionReferenceTypeFunctionName the backend is fixed at build
	// time; for FunctionReferenceTypeFunctionWeights (canary) the handler
	// picks the backend per request from the weight distribution.
	if rr.resolveResultType == resolveResultSingleFunction {
		for _, fn := range fh.functionMap {
			fh.function = fn
		}
	}

	var handler http.Handler = http.HandlerFunc(fh.handler)
	if trigger.Spec.CorsConfig != nil {
		// The allowlist's method fallback uses the trigger's methods
		// WITHOUT the OPTIONS the shape derivation appends for routing.
		handler = httpsecurity.CORSAllowlist(toAllowlistConfig(trigger.Spec.CorsConfig, triggerMethods(trigger)))(handler)
	}
	return handler
}

// buildInternalFunctionHandler constructs the internal listener's proxy
// handler for one function (the /fission-function/... target every non-HTTP
// trigger publishes to).
func (ts *HTTPTriggerSet) buildInternalFunctionHandler(fn *fv1.Function, fnTimeoutMap map[crd.CacheKeyUG]int) http.Handler {
	var streamIdleDefault time.Duration
	if ts.tsRoundTripperParams != nil {
		streamIdleDefault = ts.tsRoundTripperParams.streamIdleDefault
	}
	routeLogger := ts.logger.WithName(fn.Name)
	fh := &functionHandler{
		logger:               routeLogger,
		resolver:             ts.addressResolver,
		tapper:               ts.tapper,
		function:             fn,
		tsRoundTripperParams: ts.tsRoundTripperParams,
		isDebugEnv:           ts.isDebugEnv,
		structuredErrors:     ts.structuredErrors,
		accessLog:            ts.accessLog,
		functionTimeoutMap:   fnTimeoutMap,
		rtLogger:             routeLogger.WithName("roundtripper"),
		policyByUID: precomputePolicies(map[string]*fv1.Function{fn.Name: fn},
			fnTimeoutMap, streamIdleDefault),
		// Direct callers can go async on this path too (RFC-0024); the dispatcher's
		// own deliveries are gated out by the X-Fission-Invocation-Id guard in
		// handler(), so they still proxy synchronously and never re-enqueue.
		asyncInvoker: ts.asyncInvoker,
	}
	return http.HandlerFunc(fh.handler)
}

// newListenerMuxes creates the public + internal mux skeletons: encoded-path
// handling and the middleware chains. USE_ENCODED_PATH must be applied here
// — i.e. on EVERY build, one-shot or materialized — because the routers are
// atomically swapped on reconciliation (the CLAUDE.md gotcha).
func (ts *HTTPTriggerSet) newListenerMuxes(featureConfig *config.FeatureConfig) (public, internal *httpmux.Mux) {
	// Public listener: per-route metrics (httpmux labels each request by the
	// matched route's pattern, and records 404/405 under constant labels — so
	// path-scanning probes can't blow up Prometheus cardinality the way the
	// old raw-path fallback could). Encoded-path matching must be set on every
	// build, not once at startup, because the muxes are swapped atomically
	// (the CLAUDE.md USE_ENCODED_PATH gotcha).
	publicOpts := []httpmux.Option{httpmux.WithMetrics(metrics.HTTPRecorder{})}
	var internalOpts []httpmux.Option
	if ts.useEncodedPath {
		publicOpts = append(publicOpts, httpmux.WithEncodedPath())
		internalOpts = append(internalOpts, httpmux.WithEncodedPath())
	}

	// Panic recovery is added first so it wraps OUTERMOST (it also catches
	// panics in the auth middleware and the dispatcher). Auth runs as a
	// pre-match middleware. The internal mux deliberately omits both metrics
	// and auth: those are public-listener concerns, and its HMAC verifier is
	// wrapped by the bundle process, not here (keeps this unit-testable
	// without HMAC env state).
	panicRecover := panicRecoveryMiddleware(ts.logger)
	publicMW := []func(http.Handler) http.Handler{panicRecover}
	if featureConfig.AuthConfig.IsEnabled {
		publicMW = append(publicMW, authMiddleware(featureConfig))
	}
	publicOpts = append(publicOpts, httpmux.WithMiddleware(publicMW...))
	internalOpts = append(internalOpts, httpmux.WithMiddleware(panicRecover))

	return httpmux.New(publicOpts...), httpmux.New(internalOpts...)
}

// registerRouterOwnedRoutes adds the public listener's own endpoints: the
// GKE-ingress "/" health fallback (unless a user trigger claims GET /), the
// auth login endpoint, /router-healthz, /readyz, and /_version. All are
// deny-all-CORS: none is a legitimate browser surface.
func (ts *HTTPTriggerSet) registerRouterOwnedRoutes(public *httpmux.Mux, featureConfig *config.FeatureConfig, homeHandled bool) {
	if !homeHandled {
		// A no-op 200 for "GET /": GKE Ingress (and other ingress
		// implementations) use it as a health check, so it must not 404
		// just because no function is mapped there. OPTIONS is registered
		// alongside GET so a preflight reaches DenyAllCORS instead of
		// being 405'd by mux's method gate (same pattern below).
		public.Handle("/", httpsecurity.DenyAllCORS(http.HandlerFunc(defaultHomeHandler))).Methods(http.MethodGet, http.MethodOptions)
	}

	if featureConfig.AuthConfig.IsEnabled {
		path := featureConfig.AuthConfig.AuthUriPath
		// AuthUriPath is operator-supplied. If it is not a valid route pattern
		// (e.g. an unbalanced "{"), httpmux.Handler() would panic when this mux
		// is built — in the rebuild goroutine, outside panicRecoveryMiddleware
		// — and crash-loop the router. Validate up front and skip the login
		// route on a bad value (logged) so a misconfiguration degrades to "no
		// login endpoint" rather than a down router.
		if err := httpmux.CompilePattern(path, httpmux.Exact); err != nil {
			ts.logger.Error(err, "auth login path is not a valid route pattern; skipping the auth login route — "+
				"auth is enabled but clients cannot log in until authUriPath is fixed", "path", path)
		} else {
			public.Handle(path, httpsecurity.DenyAllCORS(http.HandlerFunc(authLoginHandler(featureConfig)))).Methods(http.MethodPost, http.MethodOptions)
		}
	}

	// Healthz stays on the public listener so existing readiness/liveness
	// probes and external monitors keep working without HMAC credentials.
	public.Handle("/router-healthz", httpsecurity.DenyAllCORS(http.HandlerFunc(routerHealthHandler))).Methods(http.MethodGet, http.MethodOptions)
	// Readiness: 200 only once the first mux build succeeded.
	public.Handle("/readyz", httpsecurity.DenyAllCORS(http.HandlerFunc(ts.routerReadinessHandler))).Methods(http.MethodGet, http.MethodOptions)
	public.Handle("/_version", httpsecurity.DenyAllCORS(http.HandlerFunc(versionHandler))).Methods(http.MethodGet, http.MethodOptions)
}
