// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/metrics"
)

// This file holds the route-shape derivation and mux-registration helpers
// shared by the two route-update paths (RFC-0013): the legacy full rebuild
// (buildMuxes, kept as the ROUTER_INCREMENTAL_ROUTES=false escape hatch) and
// the incremental materializer. Keeping them shared is what lets the golden
// shape tests guarantee both paths register identical routes.

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
// instead of gorilla's 405).
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
// up to two gorilla routes (exact and/or prefix), each gated by the shape's
// methods and optional host.
//
// gorilla's Route.Methods MUTATES the slice it is handed (it uppercases the
// entries in place) and then keeps it as the route's matcher. Passing the
// shape's slice directly would (a) corrupt the caller's canonical copy (the
// route table's spec, or the informer-owned trigger object) and (b) race
// with the still-serving previous mux whose matcher shares the same backing
// array — so each registration gets its own clone.
func registerRouteShape(r *mux.Router, shape routeShape, handler http.Handler) {
	if shape.exactPath != "" {
		route := r.Handle(shape.exactPath, handler).Methods(slices.Clone(shape.methods)...)
		if shape.host != "" {
			route.Host(shape.host)
		}
	}
	if shape.prefixPath != "" {
		route := r.PathPrefix(shape.prefixPath).Handler(handler).Methods(slices.Clone(shape.methods)...)
		if shape.host != "" {
			route.Host(shape.host)
		}
	}
}

// internalRouteShapes returns the internal listener's route pair for a
// function: the exact /fission-function/... URL and its slash subtree.
// utils.UrlForFunction folds the default namespace — the form every internal
// publisher builds.
func internalRoutePair(key types.NamespacedName) (exact, prefix string) {
	exact = utils.UrlForFunction(key.Name, key.Namespace)
	return exact, exact + "/"
}

// validateRouteTemplate reports whether a shape's gorilla path templates
// compile. Two failure classes, both reachable through admitted triggers
// (there is no HTTPTrigger admission webhook and CEL cannot run gorilla's
// parser): a capturing group ({sort:(asc|desc)}) makes gorilla PANIC at
// registration — which, unguarded, crashes the router's rebuild goroutine
// and CrashLoops the process for as long as the trigger exists — and a
// malformed template (unbalanced braces, empty var) records a route error
// that silently never matches. Both paths (legacy buildMuxes and the
// incremental apply) reject the trigger through triggerConfigError instead,
// surfacing RouteAdmitted=False/InvalidRouteTemplate.
func validateRouteTemplate(shape routeShape) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("invalid route template: %v", rec)
		}
	}()
	scratch := mux.NewRouter()
	if shape.exactPath != "" {
		if e := scratch.Handle(shape.exactPath, http.NotFoundHandler()).GetError(); e != nil {
			return e
		}
	}
	if shape.prefixPath != "" {
		if e := scratch.PathPrefix(shape.prefixPath).Handler(http.NotFoundHandler()).GetError(); e != nil {
			return e
		}
	}
	return nil
}

// buildTriggerHandler constructs the proxy handler for one trigger from its
// resolve result: the functionHandler with hoisted per-route state
// (RFC-0014) plus the per-trigger CORS wrap. fnTimeoutMap may be the global
// map (legacy path) or a per-trigger map derived from the resolved functions
// (incremental path) — the handler only ever looks up its own backends.
func (ts *HTTPTriggerSet) buildTriggerHandler(trigger *fv1.HTTPTrigger, rr *resolveResult, fnTimeoutMap map[types.UID]int) http.Handler {
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
		functionTimeoutMap:       fnTimeoutMap,
		rtLogger:                 routeLogger.WithName("roundtripper"),
		policyByUID:              precomputePolicies(rr.functionMap, fnTimeoutMap, streamIdleDefault),
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
func (ts *HTTPTriggerSet) buildInternalFunctionHandler(fn *fv1.Function, fnTimeoutMap map[types.UID]int) http.Handler {
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
		functionTimeoutMap:   fnTimeoutMap,
		rtLogger:             routeLogger.WithName("roundtripper"),
		policyByUID: precomputePolicies(map[string]*fv1.Function{fn.Name: fn},
			fnTimeoutMap, streamIdleDefault),
	}
	return http.HandlerFunc(fh.handler)
}

// newListenerMuxes creates the public + internal mux skeletons: encoded-path
// handling and the middleware chains. USE_ENCODED_PATH must be applied here
// — i.e. on EVERY build, legacy or materialized — because the routers are
// atomically swapped on reconciliation (the CLAUDE.md gotcha).
func (ts *HTTPTriggerSet) newListenerMuxes(featureConfig *config.FeatureConfig) (public, internal *mux.Router) {
	public = mux.NewRouter()
	internal = mux.NewRouter()
	if ts.useEncodedPath {
		public = public.UseEncodedPath()
		internal = internal.UseEncodedPath()
	}

	// Panic recovery is the outermost middleware so it also catches panics
	// in the metrics/auth middleware below. Applied to both listeners.
	public.Use(panicRecoveryMiddleware(ts.logger))
	internal.Use(panicRecoveryMiddleware(ts.logger))

	// The internal mux deliberately omits the metrics middleware, the auth
	// middleware, and the router-owned routes: those concerns are
	// public-listener only.
	public.Use(metrics.HTTPMetricMiddleware)
	if featureConfig.AuthConfig.IsEnabled {
		public.Use(authMiddleware(featureConfig))
	}
	return public, internal
}

// registerRouterOwnedRoutes adds the public listener's own endpoints: the
// GKE-ingress "/" health fallback (unless a user trigger claims GET /), the
// auth login endpoint, /router-healthz, /readyz, and /_version. All are
// deny-all-CORS: none is a legitimate browser surface.
func (ts *HTTPTriggerSet) registerRouterOwnedRoutes(public *mux.Router, featureConfig *config.FeatureConfig, homeHandled bool) {
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
		public.Handle(path, httpsecurity.DenyAllCORS(http.HandlerFunc(authLoginHandler(featureConfig)))).Methods(http.MethodPost, http.MethodOptions)
	}

	// Healthz stays on the public listener so existing readiness/liveness
	// probes and external monitors keep working without HMAC credentials.
	public.Handle("/router-healthz", httpsecurity.DenyAllCORS(http.HandlerFunc(routerHealthHandler))).Methods(http.MethodGet, http.MethodOptions)
	// Readiness: 200 only once the first mux build succeeded.
	public.Handle("/readyz", httpsecurity.DenyAllCORS(http.HandlerFunc(ts.routerReadinessHandler))).Methods(http.MethodGet, http.MethodOptions)
	public.Handle("/_version", httpsecurity.DenyAllCORS(http.HandlerFunc(versionHandler))).Methods(http.MethodGet, http.MethodOptions)
}
