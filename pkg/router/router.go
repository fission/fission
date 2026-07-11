// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

/*

This is the Fission Router package.

Its job is to:

  1. Keep track of HTTP triggers and their mappings to functions

     Use the Kubernetes API to get and watch this state.

  2. Given a function, get a reference to a routable function run service

     Use the ContainerPoolManager API to get a service backed by one
     or more function run containers.  The container(s) backing the
     service may be newly created, or they might be reused.  The only
     requirement is that one or more containers backs the service.

  3. Forward the request to the service, and send the response back.

     Plain ol HTTP.

*/

package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
	authorizationv1 "k8s.io/api/authorization/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/tenant"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/correlation"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/httpserver"
	fissionmetrics "github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// routerScheme is the router Manager's scheme: the Fission CRD types plus the
// Kubernetes built-ins (EndpointSlices for the RFC-0002 endpoint index).
var routerScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(routerScheme))
	utilruntime.Must(scheme.AddToScheme(routerScheme))
}

// sliceWatchNamespaces returns the namespaces the EndpointSlice informer
// watches: the function namespaces (where the function Services live).
func sliceWatchNamespaces() []string {
	return utils.DefaultNSResolver().FunctionNamespaces()
}

// routerCacheOptions scopes the Manager cache. The trigger/function watches
// stay on the Fission namespaces (crmanager.FissionCacheOptions); when the
// EndpointSlice index is enabled, the slice watch is additionally label-bound
// to Fission-managed slices and scoped to the function namespaces (where the
// function Services live), keeping the informer memory proportional to
// Fission's own objects.
func routerCacheOptions(mode endpointSliceCacheMode) crcache.Options {
	opts := crmanager.FissionCacheOptions()
	if mode == endpointSliceCacheOff {
		return opts
	}
	sliceByObject := crcache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE}),
	}
	// Cluster mode: functions (and their Services) live in any namespace, so the
	// label-bounded slice watch goes cluster-wide (no per-namespace scoping). Other
	// modes scope it to the function namespaces where the Services live.
	if !utils.ClusterTenancyEnabled() {
		sliceNS := map[string]crcache.Config{}
		for _, ns := range sliceWatchNamespaces() {
			sliceNS[ns] = crcache.Config{}
		}
		sliceByObject.Namespaces = sliceNS
	}
	opts.ByObject = map[client.Object]crcache.ByObject{
		&discoveryv1.EndpointSlice{}: sliceByObject,
	}
	return opts
}

// checkSliceWatchRBAC verifies — with an actionable error — that the router
// can list and watch EndpointSlices in every namespace the slice informer
// would watch. Without this preflight a missing Role leaves the manager cache
// retrying a forbidden LIST forever: the router hangs not-ready and the only
// symptom is a reflector error log. The chart renders the required Role +
// RoleBinding (router/role-dataplane.yaml) whenever
// router.endpointSliceCache.mode != off; bespoke-RBAC installs must mirror it.
//
// Callers degrade to mode=off on error rather than exiting: the slice cache is
// a warm-path optimization with a full legacy fallback, and crash-looping the
// data plane over a missing optimization grant (e.g. a GitOps prune dropping
// the Role) would turn it into an outage.
func checkSliceWatchRBAC(ctx context.Context, kubeClient kubernetes.Interface) error {
	// Cluster mode watches EndpointSlices cluster-wide, so a single cluster-scoped
	// review (empty Namespace) is the right preflight; other modes check each
	// function namespace the informer scopes to.
	watchNamespaces := sliceWatchNamespaces()
	if utils.ClusterTenancyEnabled() {
		watchNamespaces = []string{""}
	}
	for _, ns := range watchNamespaces {
		for _, verb := range []string{"list", "watch"} {
			sar := &authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Namespace: ns,
						Verb:      verb,
						Group:     "discovery.k8s.io",
						Resource:  "endpointslices",
					},
				},
			}
			// Retry the review itself: a transient apiserver error during boot
			// must not degrade the data plane for the router's whole lifetime.
			// Only an explicit Allowed=false (genuinely missing RBAC) or a
			// persistent API failure degrades.
			var res *authorizationv1.SelfSubjectAccessReview
			var err error
			for attempt := range 3 {
				if attempt > 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(2 * time.Second):
					}
				}
				res, err = kubeClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
				if err == nil {
					break
				}
			}
			if err != nil {
				return fmt.Errorf("error checking endpointslice RBAC in namespace %q: %w", ns, err)
			}
			if !res.Status.Allowed {
				return fmt.Errorf("router is not allowed to %s endpointslices in namespace %q "+
					"(reason: %s); the Helm chart renders the required router-dataplane Role for "+
					"router.endpointSliceCache.mode != off — grant the RBAC to enable the EndpointSlice data plane", verb, ns, res.Status.Reason)
			}
		}
	}
	return nil
}

// runnableFunc adapts a function to a controller-runtime manager.Runnable.
type runnableFunc func(context.Context) error

func (f runnableFunc) Start(ctx context.Context) error { return f(ctx) }

// internalListenerMaxBodyBytes caps the request body size the HMAC
// verifier on the internal listener will buffer. 64 MiB is large enough
// for any realistic JSON / form / small-binary payload that flows from
// timer / kubewatcher / mqtrigger / executor through
// /fission-function/<ns>/<name>, while still bounding the cost of a
// malicious oversized request before the signature check runs. This
// trims significantly below storagesvc's 256 MiB ceiling because
// function-invocation bodies are typically small request payloads, not
// archive uploads.
const internalListenerMaxBodyBytes int64 = 64 << 20

// request url ---[mux]---> Function(name,uid) ----[fmap]----> k8s service url

// request url ---[trigger]---> Function(name, deployment) ----[deployment]----> Function(name, uid) ----[pool mgr]---> k8s service url

// router constructs the public and internal mutable routers and wires
// them to httpTriggerSet's reconciliation loop. Both routers are
// initialised with empty httpmux handlers (everything 404s); the trigger set
// fills them in on first sync. The USE_ENCODED_PATH setting (see issue
// https://github.com/fission/fission/issues/1317) is applied by
// newListenerMuxes on every reconciliation rather than here, so that the
// feature stays on across the atomic mux swaps.
func router(ctx context.Context, logger logr.Logger, mgr *errgroup.Group, httpTriggerSet *HTTPTriggerSet) (*mutableRouter, *mutableRouter, error) {
	publicMR := newMutableRouter(logger, httpmux.New().Handler())
	internalMR := newMutableRouter(logger.WithName("internal"), httpmux.New().Handler())

	err := httpTriggerSet.subscribeRouter(ctx, mgr, publicMR, internalMR)
	if err != nil {
		return nil, nil, err
	}
	return publicMR, internalMR, nil
}

func serve(ctx context.Context, logger logr.Logger, mgr *errgroup.Group, opts Options,
	httpTriggerSet *HTTPTriggerSet) error {
	publicMR, internalMR, err := router(ctx, logger, mgr, httpTriggerSet)
	if err != nil {
		return fmt.Errorf("error making router: %w", err)
	}

	// SecurityHeaders wraps the entire public listener so every
	// response — router-owned routes (healthz / version / auth) and
	// user-trigger proxies alike — carries X-Content-Type-Options:
	// nosniff and Vary: Origin. Per-route DenyAllCORS for router-owned
	// routes is wired inside buildMuxes; user-trigger routes do NOT
	// get DenyAllCORS so user functions remain free to emit their own
	// CORS responses (opt-in HTTPTrigger.CorsConfig lands in a follow-up).
	// correlation.Middleware sits inside the OTEL handler so it observes the
	// extracted SpanContext, and honors/mints X-Fission-Request-ID for every
	// request — user-trigger proxies and router-owned routes alike.
	publicHandler := httpsecurity.SecurityHeaders(
		otelUtils.GetHandlerWithOTEL(correlation.Middleware(publicMR), "fission-router", otelUtils.UrlsToIgnore("/router-healthz")),
	)
	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{
			Name: "router", Addr: strconv.Itoa(opts.Port), Listener: opts.Listener, Handler: publicHandler,
		})
		return nil
	})

	// Internal listener for /fission-function/<ns>/<name>. We wrap the
	// mutable router with the HMAC verifier so an attacker who somehow
	// reaches port 8889 (NetworkPolicy-locked to executor / kubewatcher
	// / timer / mqtrigger) without a valid signature is rejected
	// before reaching the function-handler proxy. An empty
	// FISSION_INTERNAL_AUTH_SECRET is the explicit pass-through mode
	// for first-deploy / migration installs and is safe by virtue of
	// the NetworkPolicy still gating the port.
	master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	masterOld := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD"))
	// correlation.Middleware is wrapped inside both the OTEL handler and the
	// HMAC verifier (added below): the verifier still signs only method + URI +
	// body, so the request-id header it sets post-verification never
	// participates in the signature.
	internalHandlerInner := otelUtils.GetHandlerWithOTEL(correlation.Middleware(internalMR), "fission-router-internal")
	// Use the per-service derived key for ServiceRouterInternal so a
	// leak of the router's runtime memory cannot forge requests on
	// other Fission internal channels (storagesvc, fetcher, builder,
	// executor). See docs/internal-auth/00-design.md.
	verifier := hmacauth.ServiceVerifier(master, masterOld, hmacauth.ServiceRouterInternal, hmacauth.VerifierOpts{
		SkewSec: 60,
		// Cap the request body the verifier will buffer before the
		// signature check; see internalListenerMaxBodyBytes for the
		// rationale behind 64 MiB.
		MaxBodyBytes: internalListenerMaxBodyBytes,
		Logger:       logger.WithName("internal-hmac"),
	})
	// DenyAllCORS wraps the verifier so a browser-driven cross-origin
	// preflight against this listener is rejected with 403 before HMAC
	// even buffers the body. SecurityHeaders is outermost so every
	// response (including 401s from the verifier) carries nosniff and
	// Vary: Origin. The internal listener has no legitimate browser
	// caller — kubewatcher, timer, mqtrigger, canaryconfigmgr, executor,
	// and buildermgr all run pod-to-pod.
	internalHandler := httpsecurity.SecurityHeaders(
		httpsecurity.DenyAllCORS(verifier(internalHandlerInner)),
	)
	mgr.Go(func() error {
		httpserver.Serve(ctx, logger, mgr, httpserver.ServerOptions{
			Name: "router-internal", Addr: strconv.Itoa(opts.InternalPort), Listener: opts.InternalListener, Handler: internalHandler,
		})
		return nil
	})

	return nil
}

// Options configures StartWithOptions. Each listener is either pre-bound by
// the caller (Listener/InternalListener — e.g. a test harness binding
// 127.0.0.1:0) or bound here from the corresponding port.
type Options struct {
	// Port is the public listener port (user HTTPTriggers, /router-healthz,
	// /_version). Ignored when Listener is set.
	Port int
	// InternalPort is the port for the listener serving
	// /fission-function/<ns>/<name> behind the HMAC verifier
	// (GHSA-3g33-6vg6-27m8 split). Zero or negative values default to
	// svcinfo.PortRouterInternal. Ignored when InternalListener is set.
	InternalPort int
	// Listener optionally pre-binds the public listener.
	Listener net.Listener
	// InternalListener optionally pre-binds the internal listener.
	InternalListener net.Listener
	// Executor is the executor API client used on cache misses.
	Executor eclient.ClientInterface
}

// Start starts a router on the given ports. See StartWithOptions.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group, port int, internalPort int, executor eclient.ClientInterface) error {
	return StartWithOptions(ctx, clientGen, logger, mgr, Options{Port: port, InternalPort: internalPort, Executor: executor})
}

// StartWithOptions starts a router. The internal listener is mandatory —
// the public listener no longer registers /fission-function/... routes.
func StartWithOptions(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group, opts Options) error {
	executor := opts.Executor
	if opts.InternalListener == nil && opts.InternalPort <= 0 {
		opts.InternalPort = svcinfo.PortRouterInternal
	}
	// Same-port collision is only possible when both listeners are bound
	// here from ports; pre-bound listeners are distinct by construction.
	if opts.Listener == nil && opts.InternalListener == nil && opts.InternalPort == opts.Port {
		return fmt.Errorf("router internal port (%d) must differ from public port (%d)", opts.InternalPort, opts.Port)
	}
	fmap := makeFunctionServiceMap(logger, time.Minute)

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("error making the fission client: %w", err)
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("error making the kube client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("error getting rest config: %w", err)
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	cfg, err := loadRouterConfig(logger)
	if err != nil {
		return err
	}

	// The slice informer needs read RBAC in the function namespaces (the chart
	// renders it when the mode isn't off). A missing grant would wedge the
	// manager cache sync and hang the router not-ready, so degrade to the
	// legacy data plane loudly instead. Checked before manager construction
	// because the cache options depend on the (possibly downgraded) mode.
	// The pooled proxy transport's effective settings (RFC-0014): the one log
	// line an operator can check to confirm keep-alive / pool sizing took
	// effect (e.g. after using the disableKeepAlive escape hatch).
	effPerHost := cfg.maxIdleConnsPerHost
	if effPerHost <= 0 {
		effPerHost = defaultMaxIdleConnsPerHost
	}
	logger.Info("router proxy transport configured",
		"disableKeepAlive", cfg.disableKeepAlive,
		"maxIdleConnsPerHost", effPerHost,
		"idleConnTimeout", transportIdleConnTimeout)

	requestedMode := cfg.endpointSliceCacheMode
	if cfg.endpointSliceCacheMode != endpointSliceCacheOff {
		if rerr := checkSliceWatchRBAC(ctx, kubeClient); rerr != nil {
			logger.Error(rerr, "disabling the EndpointSlice cache (degrading to the executor-RPC data plane)",
				"requested_mode", cfg.endpointSliceCacheMode)
			cfg.endpointSliceCacheMode = endpointSliceCacheOff
		}
	}
	// Registered unconditionally so the requested-vs-effective mode is
	// alertable: an absent series cannot distinguish "mode=off install" from
	// "RBAC degrade silently turned the data plane off after a restart".
	endpointcache.RegisterModeInfo(string(requestedMode), string(cfg.endpointSliceCacheMode), cfg.endpointSliceEndpointLB)

	// The router runs under a controller-runtime Manager for lifecycle
	// consistency with the rest of the control plane and to host the HTTPTrigger
	// + Function reconcilers. It is stateless and replica-independent, so it
	// uses NO leader election (every replica serves and reconciles its own mux).
	// The Manager owns the metrics server and graceful shutdown; /router-healthz
	// + /readyz stay on the public listener, so the Manager's own health
	// server is disabled.
	var alreadyRegistered prometheus.AlreadyRegisteredError
	if err := ctrlmetrics.Registry.Register(fissionmetrics.Registry); err != nil && !errors.As(err, &alreadyRegistered) {
		logger.Error(err, "failed to register fission metrics collectors")
	}

	metricsBind := httpserver.BindAddrFromEnv("METRICS_ADDR", svcinfo.PortMetrics)

	crMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: routerScheme,
		// Scope the shared cache to the Fission-watched namespaces. The
		// reconcilers and the incremental resync read HTTPTriggers + Functions
		// through it, and the router's RBAC is per-namespace Roles (not a ClusterRole) — a
		// cluster-wide cache's list/watch is forbidden, so its sync would time out
		// and the manager would exit. See routerCacheOptions.
		Cache:                  routerCacheOptions(cfg.endpointSliceCacheMode),
		Metrics:                metricsserver.Options{BindAddress: metricsBind},
		HealthProbeBindAddress: "0", // /router-healthz + /readyz stay on the public listener
		LeaderElection:         false,
		Logger:                 logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up router manager: %w", err)
	}

	triggers, err := makeHTTPTriggerSet(logger.WithName("triggerset"), fmap, fissionClient, kubeClient, crMgr.GetClient(), executor, &tsRoundTripperParams{
		timeout:             cfg.roundTripTimeout,
		timeoutExponent:     cfg.timeoutExponent,
		disableKeepAlive:    cfg.disableKeepAlive,
		keepAliveTime:       cfg.keepAliveTime,
		maxRetries:          cfg.maxRetries,
		svcAddrRetryCount:   cfg.svcAddrRetryCount,
		streamIdleDefault:   cfg.streamIdleDefault,
		maxIdleConnsPerHost: cfg.maxIdleConnsPerHost,
	}, cfg.isDebugEnv, cfg.useEncodedPath, cfg.unTapServiceTimeout, throttler.MakeThrottler(cfg.svcAddrUpdateTimeout))
	if err != nil {
		return fmt.Errorf("error making HTTP trigger set: %w", err)
	}
	// Structured failure-attribution error bodies (RFC-0015). Set on the
	// trigger set (not threaded through the constructor) so every functionHandler
	// it builds inherits it; the escape hatch restores the legacy plain-text body.
	triggers.structuredErrors = cfg.structuredErrors
	triggers.accessLog = cfg.accessLog
	// Incremental route updates (RFC-0013) are the only production path:
	// per-event route-table diffs + handler indirection; muxes rebuild only on
	// shape changes.
	triggers.initIncrementalRoutes()

	// EndpointSlice-fed endpoint index (RFC-0002). Every router replica watches
	// independently (no leader election — that is the point: warm-path state is
	// replica-local). The fallback resolver serves the warm path from the
	// index and uses the executor for cold starts, capacity, and strict-mode
	// functions.
	if cfg.endpointSliceCacheMode != endpointSliceCacheOff {
		index := endpointcache.NewIndex()
		if err := endpointcache.RegisterInformer(ctx, crMgr, index, logger); err != nil {
			return fmt.Errorf("error registering endpointslice informer: %w", err)
		}
		endpointcache.RegisterSizeGauge(index)
		execResolver, ok := triggers.addressResolver.(*executorResolver)
		if !ok {
			return fmt.Errorf("unexpected address resolver type %T", triggers.addressResolver)
		}
		switch cfg.endpointSliceCacheMode {
		case endpointSliceCacheOn:
			// The client interface carries EnsureCapacity since phase 4; an
			// OLD executor (predating /v2/ensureCapacity) still degrades at
			// runtime via the 404 → legacy-RPC fallback in the resolver.
			triggers.addressResolver = newFallbackResolver(logger, index, execResolver, executor, cfg.endpointSliceEndpointLB)
		default:
			// Unreachable: loadRouterConfig validates the mode. The guard
			// keeps a future refactor from silently paying for the informer
			// while leaving the legacy resolver wired.
			return fmt.Errorf("unhandled endpointslice cache mode %q", cfg.endpointSliceCacheMode)
		}
		logger.Info("endpointslice cache enabled", "mode", cfg.endpointSliceCacheMode, "endpoint_lb", cfg.endpointSliceEndpointLB)
	}

	// Build the route providers. The ingress provider is always registered (it
	// serves the deprecated CreateIngress path); the gateway provider is added
	// only when GATEWAY_API_ENABLED is set, so its RBAC is needed only when
	// opted in. A trigger that requests a provider the router did not register
	// gets no route object (and, in a later phase, a status Condition).
	providers, err := buildRouteProviders(logger, kubeClient, restConfig)
	if err != nil {
		return fmt.Errorf("error building route providers: %w", err)
	}

	// Register the trigger + function reconcilers. Each signals a debounced mux
	// rebuild; GenerationChangedPredicate drops status-only writes so the
	// router's own HTTPTrigger condition writes don't loop.
	if err := controller.RegisterTenantScoped(crMgr, &fv1.HTTPTrigger{},
		&httpTriggerReconciler{logger: logger.WithName("httptrigger_reconciler"), client: crMgr.GetClient(), ts: triggers, providers: providers},
		"router-httptrigger"); err != nil {
		return fmt.Errorf("error registering httptrigger reconciler: %w", err)
	}
	if err := controller.RegisterTenantScoped(crMgr, &fv1.Function{},
		&functionReconciler{logger: logger.WithName("function_reconciler"), client: crMgr.GetClient(), ts: triggers},
		"router-function"); err != nil {
		return fmt.Errorf("error registering function reconciler: %w", err)
	}

	// Cross-process propagation: under dynamic tenancy, keep the router's resolver
	// in step with the FissionTenant set so a namespace onboarded at runtime is
	// admitted by the tenant-scoped reconcilers above (their MembershipPredicate
	// reads this resolver) and its HTTPTriggers start routing without a restart.
	// The router's cache is already cluster-wide in this mode (FissionCacheOptions).
	// AddResolverSync is a no-op when dynamic tenancy is off.
	if err := tenant.AddResolverSync(crMgr); err != nil {
		return fmt.Errorf("error registering tenant resolver-sync: %w", err)
	}

	logger.Info("starting router", "port", opts.Port, "internalPort", opts.InternalPort)

	// The public/internal listeners run on an internal GroupManager, hosted by a
	// single Manager runnable.
	err = crMgr.Add(runnableFunc(func(rctx context.Context) error {
		gm := &errgroup.Group{}
		tracer := otel.Tracer("router")
		rctx, span := tracer.Start(rctx, "router/serve")
		defer span.End()
		if err := serve(rctx, logger, gm, opts, triggers); err != nil {
			return err
		}
		// Kick the initial mux build once the cache has synced, so router-owned
		// routes (healthz/version/auth) are installed even with zero triggers.
		// The reconcilers also fire for every existing object; the debouncer
		// coalesces these into a single rebuild.
		if crMgr.GetCache().WaitForCacheSync(rctx) {
			// Initial table population; idempotent against the reconciler
			// replay that is happening concurrently. The explicit signal
			// covers the zero-object install (router-owned routes must
			// still materialize) and the resync loop is the drift guard.
			if err := triggers.resync(rctx, true); err != nil {
				logger.Error(err, "initial route table resync failed; reconciler replay will converge the table")
			}
			triggers.signalMaterialize()
			gm.Go(func() error {
				triggers.resyncLoop(rctx)
				return nil
			})
		}
		<-rctx.Done()
		_ = gm.Wait()
		return nil
	}))
	if err != nil {
		return fmt.Errorf("unable to add router runnable: %w", err)
	}

	return crMgr.Start(ctx)
}
