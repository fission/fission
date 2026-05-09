/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
	"go.opentelemetry.io/otel"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/crd"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

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
// them in on first sync.
func router(ctx context.Context, logger logr.Logger, mgr manager.Interface, httpTriggerSet *HTTPTriggerSet) (*mutableRouter, *mutableRouter, error) {
	publicMux := mux.NewRouter()
	publicMux.Use(metrics.HTTPMetricMiddleware)
	internalMux := mux.NewRouter()

	// see issue https://github.com/fission/fission/issues/1317
	useEncodedPath, err := strconv.ParseBool(os.Getenv("USE_ENCODED_PATH"))
	if err != nil {
		return nil, nil, err
	}
	var publicMR, internalMR *mutableRouter
	if useEncodedPath {
		publicMR = newMutableRouter(logger, publicMux.UseEncodedPath())
		internalMR = newMutableRouter(logger.WithName("internal"), internalMux.UseEncodedPath())
	} else {
		publicMR = newMutableRouter(logger, publicMux)
		internalMR = newMutableRouter(logger.WithName("internal"), internalMux)
	}

	err = httpTriggerSet.subscribeRouter(ctx, mgr, publicMR, internalMR)
	if err != nil {
		return nil, nil, err
	}
	return publicMR, internalMR, nil
}

func serve(ctx context.Context, logger logr.Logger, mgr manager.Interface, port int, internalPort int,
	httpTriggerSet *HTTPTriggerSet) error {
	publicMR, internalMR, err := router(ctx, logger, mgr, httpTriggerSet)
	if err != nil {
		return fmt.Errorf("error making router: %w", err)
	}

	publicHandler := otelUtils.GetHandlerWithOTEL(publicMR, "fission-router", otelUtils.UrlsToIgnore("/router-healthz"))
	mgr.Add(ctx, func(ctx context.Context) {
		httpserver.StartServer(ctx, logger, mgr, "router", fmt.Sprintf("%d", port), publicHandler)
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
	internalHandler := verifier(internalHandlerInner)
	mgr.Add(ctx, func(ctx context.Context) {
		httpserver.StartServer(ctx, logger, mgr, "router-internal", fmt.Sprintf("%d", internalPort), internalHandler)
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
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr manager.Interface, port int, internalPort int, executor eclient.ClientInterface) error {
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

	triggers, err := makeHTTPTriggerSet(logger.WithName("triggerset"), fmap, fissionClient, kubeClient, executor, &tsRoundTripperParams{
		timeout:           timeout,
		timeoutExponent:   timeoutExponent,
		disableKeepAlive:  disableKeepAlive,
		keepAliveTime:     keepAliveTime,
		maxRetries:        maxRetries,
		svcAddrRetryCount: svcAddrRetryCount,
	}, isDebugEnv, unTapServiceTimeout, throttler.MakeThrottler(svcAddrUpdateTimeout))
	if err != nil {
		return fmt.Errorf("error making HTTP trigger set: %w", err)
	}

	mgr.Add(ctx, func(ctx context.Context) {
		metrics.ServeMetrics(ctx, "router", logger, mgr)
	})

	logger.Info("starting router", "port", port, "internalPort", internalPort)

	tracer := otel.Tracer("router")
	ctx, span := tracer.Start(ctx, "router/Start")
	defer span.End()

	return serve(ctx, logger, mgr, port, internalPort, triggers)
}
