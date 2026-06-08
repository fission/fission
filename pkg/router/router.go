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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/httpserver"
	fissionmetrics "github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// runnableFunc adapts a function to a controller-runtime manager.Runnable.
type runnableFunc func(context.Context) error

func (f runnableFunc) Start(ctx context.Context) error { return f(ctx) }

// bindAddr resolves a server bind address from env, defaulting to def and
// prefixing ":" when only a port is given.
func bindAddr(env, def string) string {
	addr := os.Getenv(env)
	if addr == "" {
		addr = def
	}
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	return addr
}

// DefaultInternalListenerPort is the default port for the internal
// listener that serves /fission-function/<ns>/<name>. It must match the
// targetPort used by the chart's router Service "internal" port.
const DefaultInternalListenerPort = 8889

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
// initialised with empty mux.Router instances; the trigger set fills
// them in on first sync. The USE_ENCODED_PATH setting (see issue
// https://github.com/fission/fission/issues/1317) is applied by
// httpTriggerSet.buildMuxes on every reconciliation rather than here,
// so that the feature stays on across the atomic mux swaps.
func router(ctx context.Context, logger logr.Logger, mgr *errgroup.Group, httpTriggerSet *HTTPTriggerSet) (*mutableRouter, *mutableRouter, error) {
	publicMR := newMutableRouter(logger, mux.NewRouter())
	internalMR := newMutableRouter(logger.WithName("internal"), mux.NewRouter())

	err := httpTriggerSet.subscribeRouter(ctx, mgr, publicMR, internalMR)
	if err != nil {
		return nil, nil, err
	}
	return publicMR, internalMR, nil
}

func serve(ctx context.Context, logger logr.Logger, mgr *errgroup.Group, port int, internalPort int,
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
	publicHandler := httpsecurity.SecurityHeaders(
		otelUtils.GetHandlerWithOTEL(publicMR, "fission-router", otelUtils.UrlsToIgnore("/router-healthz")),
	)
	mgr.Go(func() error {
		httpserver.StartServer(ctx, logger, mgr, "router", fmt.Sprintf("%d", port), publicHandler)
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
	internalHandlerInner := otelUtils.GetHandlerWithOTEL(internalMR, "fission-router-internal")
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
		httpserver.StartServer(ctx, logger, mgr, "router-internal", fmt.Sprintf("%d", internalPort), internalHandler)
		return nil
	})

	return nil
}

// Start starts a router. internalPort is the listener that serves
// /fission-function/<ns>/<name> and is wrapped with the HMAC verifier;
// pass DefaultInternalListenerPort to use the default. Zero or
// negative values are silently substituted with DefaultInternalListenerPort
// so callers can omit the flag and still get the GHSA-3g33-6vg6-27m8
// listener split — the public listener no longer registers those
// routes, so the internal listener is mandatory.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group, port int, internalPort int, executor eclient.ClientInterface) error {
	if internalPort <= 0 {
		internalPort = DefaultInternalListenerPort
	}
	if internalPort == port {
		return fmt.Errorf("router internal port (%d) must differ from public port (%d)", internalPort, port)
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

	timeoutStr := os.Getenv("ROUTER_ROUND_TRIP_TIMEOUT")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return fmt.Errorf("failed to parse timeout duration value('%s') from 'ROUTER_ROUND_TRIP_TIMEOUT': %w", timeoutStr, err)
	}

	timeoutExponentStr := os.Getenv("ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT")
	timeoutExponent, err := strconv.Atoi(timeoutExponentStr)
	if err != nil {
		return fmt.Errorf("failed to parse timeout exponent value('%s') from 'ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT': %w", timeoutExponentStr, err)
	}

	keepAliveTimeStr := os.Getenv("ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME")
	keepAliveTime, err := time.ParseDuration(keepAliveTimeStr)
	if err != nil {
		return fmt.Errorf("failed to parse keep alive duration value('%s') from 'ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME': %w", keepAliveTimeStr, err)
	}

	disableKeepAliveStr := os.Getenv("ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE")
	disableKeepAlive, err := strconv.ParseBool(disableKeepAliveStr)
	if err != nil {
		return fmt.Errorf("failed to parse enable keep alive value('%s') from 'ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE': %w", disableKeepAliveStr, err)
	}

	maxRetriesStr := os.Getenv("ROUTER_ROUND_TRIP_MAX_RETRIES")
	maxRetries, err := strconv.Atoi(maxRetriesStr)
	if err != nil {
		return fmt.Errorf("failed to parse max retries value('%s') from 'ROUTER_ROUND_TRIP_MAX_RETRIES': %w", maxRetriesStr, err)
	}

	isDebugEnvStr := os.Getenv("DEBUG_ENV")
	isDebugEnv, err := strconv.ParseBool(isDebugEnvStr)
	if err != nil {
		return fmt.Errorf("failed to parse debug env value('%s') from 'DEBUG_ENV': %w", isDebugEnvStr, err)
	}

	// svcAddrRetryCount is the max times for RetryingRoundTripper to retry with a specific service address
	svcAddrRetryCountStr := os.Getenv("ROUTER_SVC_ADDRESS_MAX_RETRIES")
	svcAddrRetryCount, err := strconv.Atoi(svcAddrRetryCountStr)
	if err != nil {
		svcAddrRetryCount = 5
		logger.Error(err, "failed to parse service address retry count from 'ROUTER_SVC_ADDRESS_MAX_RETRIES' - set to the default value", "value", svcAddrRetryCountStr,
			"default", svcAddrRetryCount)
	}

	// svcAddrUpdateTimeout is the timeout setting for a goroutine to wait for the update of a service entry.
	// If the update process cannot be done within the timeout window, consider it failed.
	svcAddrUpdateTimeoutStr := os.Getenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT")
	svcAddrUpdateTimeout, err := time.ParseDuration(os.Getenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT"))
	if err != nil {
		svcAddrUpdateTimeout = 30 * time.Second
		logger.Error(err, "failed to parse service address update timeout duration from 'ROUTER_ROUND_TRIP_SVC_ADDRESS_UPDATE_TIMEOUT' - set to the default value", "value", svcAddrUpdateTimeoutStr,
			"default", svcAddrUpdateTimeout)
	}

	// unTapServiceTimeout is the timeout used as timeout in the request context of unTapService
	unTapServiceTimeoutstr := os.Getenv("ROUTER_UNTAP_SERVICE_TIMEOUT")
	unTapServiceTimeout, err := time.ParseDuration(unTapServiceTimeoutstr)
	if err != nil {
		unTapServiceTimeout = 3600 * time.Second
		logger.Error(err, "failed to parse unTap service timeout duration from 'ROUTER_UNTAP_SERVICE_TIMEOUT' - set to the default value", "value", unTapServiceTimeoutstr,
			"default", unTapServiceTimeout)
	}

	// see issue https://github.com/fission/fission/issues/1317
	useEncodedPath, err := strconv.ParseBool(os.Getenv("USE_ENCODED_PATH"))
	if err != nil {
		return fmt.Errorf("failed to parse USE_ENCODED_PATH: %w", err)
	}

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

	metricsBind := bindAddr("METRICS_ADDR", "8080")
	if ephemeral, _ := strconv.ParseBool(os.Getenv("FISSION_TEST_EPHEMERAL_SERVERS")); ephemeral {
		// In-process e2e harness: bind an ephemeral metrics port to avoid clashes.
		metricsBind = ":0"
	}

	crMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: scheme.Scheme,
		// Scope the shared cache to the Fission-watched namespaces. The
		// reconcilers and updateRouter read HTTPTriggers + Functions through it,
		// and the router's RBAC is per-namespace Roles (not a ClusterRole) — a
		// cluster-wide cache's list/watch is forbidden, so its sync would time out
		// and the manager would exit. See crmanager.FissionCacheOptions.
		Cache:                  crmanager.FissionCacheOptions(),
		Metrics:                metricsserver.Options{BindAddress: metricsBind},
		HealthProbeBindAddress: "0", // /router-healthz + /readyz stay on the public listener
		LeaderElection:         false,
		Logger:                 logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up router manager: %w", err)
	}

	triggers, err := makeHTTPTriggerSet(logger.WithName("triggerset"), fmap, fissionClient, kubeClient, crMgr.GetClient(), executor, &tsRoundTripperParams{
		timeout:           timeout,
		timeoutExponent:   timeoutExponent,
		disableKeepAlive:  disableKeepAlive,
		keepAliveTime:     keepAliveTime,
		maxRetries:        maxRetries,
		svcAddrRetryCount: svcAddrRetryCount,
	}, isDebugEnv, useEncodedPath, unTapServiceTimeout, throttler.MakeThrottler(svcAddrUpdateTimeout))
	if err != nil {
		return fmt.Errorf("error making HTTP trigger set: %w", err)
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
	if err := controller.Register(crMgr, &fv1.HTTPTrigger{},
		&httpTriggerReconciler{logger: logger.WithName("httptrigger_reconciler"), client: crMgr.GetClient(), ts: triggers, providers: providers},
		"router-httptrigger"); err != nil {
		return fmt.Errorf("error registering httptrigger reconciler: %w", err)
	}
	if err := controller.Register(crMgr, &fv1.Function{},
		&functionReconciler{logger: logger.WithName("function_reconciler"), client: crMgr.GetClient(), ts: triggers},
		"router-function"); err != nil {
		return fmt.Errorf("error registering function reconciler: %w", err)
	}

	logger.Info("starting router", "port", port, "internalPort", internalPort)

	// The public/internal listeners run on an internal GroupManager, hosted by a
	// single Manager runnable.
	err = crMgr.Add(runnableFunc(func(rctx context.Context) error {
		gm := &errgroup.Group{}
		tracer := otel.Tracer("router")
		rctx, span := tracer.Start(rctx, "router/serve")
		defer span.End()
		if err := serve(rctx, logger, gm, port, internalPort, triggers); err != nil {
			return err
		}
		// Kick the initial mux build once the cache has synced, so router-owned
		// routes (healthz/version/auth) are installed even with zero triggers.
		// The reconcilers also fire for every existing object; the debouncer
		// coalesces these into a single rebuild.
		if crMgr.GetCache().WaitForCacheSync(rctx) {
			triggers.syncTriggers()
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
