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

	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/fission/fission/pkg/crd"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// request url ---[mux]---> Function(name,uid) ----[fmap]----> k8s service url

// request url ---[trigger]---> Function(name, deployment) ----[deployment]----> Function(name, uid) ----[pool mgr]---> k8s service url

func router(ctx context.Context, logger *zap.Logger, mgr manager.Interface, httpTriggerSet *HTTPTriggerSet) (*mutableRouter, error) {
	var mr *mutableRouter
	mux := mux.NewRouter()
	mux.Use(metrics.HTTPMetricMiddleware)

	// see issue https://github.com/fission/fission/issues/1317
	useEncodedPath, err := strconv.ParseBool(os.Getenv("USE_ENCODED_PATH"))
	if err != nil {
		return nil, err
	}
	if useEncodedPath {
		mr = newMutableRouter(logger, mux.UseEncodedPath())
	} else {
		mr = newMutableRouter(logger, mux)
	}

	err = httpTriggerSet.subscribeRouter(ctx, mgr, mr)
	if err != nil {
		return nil, err
	}
	return mr, nil
}

func serve(ctx context.Context, logger *zap.Logger, mgr manager.Interface, port int,
	httpTriggerSet *HTTPTriggerSet, displayAccessLog bool) error {
	mr, err := router(ctx, logger, mgr, httpTriggerSet)
	if err != nil {
		return errors.Wrap(err, "error making router")
	}
	handler := otelUtils.GetHandlerWithOTEL(mr, "fission-router", otelUtils.UrlsToIgnore("/router-healthz"))
	mgr.Add(ctx, func(ctx context.Context) {
		httpserver.StartServer(ctx, logger, mgr, "router", fmt.Sprintf("%d", port), handler)
	})
	return nil
}

// Start starts a router
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, port int, executor eclient.ClientInterface) error {
	fmap := makeFunctionServiceMap(logger, time.Minute)

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return errors.Wrap(err, "error making the fission client")
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return errors.Wrap(err, "error making the kube client")
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return errors.Wrap(err, "error waiting for CRDs")
	}

	timeoutStr := os.Getenv("ROUTER_ROUND_TRIP_TIMEOUT")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to parse timeout duration value('%s') from 'ROUTER_ROUND_TRIP_TIMEOUT'", timeoutStr))
	}

	timeoutExponentStr := os.Getenv("ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT")
	timeoutExponent, err := strconv.Atoi(timeoutExponentStr)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to parse timeout exponent value('%s') from 'ROUTER_ROUNDTRIP_TIMEOUT_EXPONENT'", timeoutExponentStr))
	}

	keepAliveTimeStr := os.Getenv("ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME")
	keepAliveTime, err := time.ParseDuration(keepAliveTimeStr)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to parse keep alive duration value('%s') from 'ROUTER_ROUND_TRIP_KEEP_ALIVE_TIME'", keepAliveTimeStr))
	}

	disableKeepAliveStr := os.Getenv("ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE")
	disableKeepAlive, err := strconv.ParseBool(disableKeepAliveStr)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to parse enable keep alive value('%s') from 'ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE'", disableKeepAliveStr))
	}

	maxRetriesStr := os.Getenv("ROUTER_ROUND_TRIP_MAX_RETRIES")
	maxRetries, err := strconv.Atoi(maxRetriesStr)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to parse max retries value('%s') from 'ROUTER_ROUND_TRIP_MAX_RETRIES'", maxRetriesStr))
	}

	isDebugEnvStr := os.Getenv("DEBUG_ENV")
	isDebugEnv, err := strconv.ParseBool(isDebugEnvStr)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to parse debug env value('%s') from 'DEBUG_ENV'", isDebugEnvStr))
	}

	// svcAddrRetryCount is the max times for RetryingRoundTripper to retry with a specific service address
	svcAddrRetryCountStr := os.Getenv("ROUTER_SVC_ADDRESS_MAX_RETRIES")
	svcAddrRetryCount, err := strconv.Atoi(svcAddrRetryCountStr)
	if err != nil {
		svcAddrRetryCount = 5
		logger.Error("failed to parse service address retry count from 'ROUTER_SVC_ADDRESS_MAX_RETRIES' - set to the default value",
			zap.Error(err),
			zap.String("value", svcAddrRetryCountStr),
			zap.Int("default", svcAddrRetryCount))
	}

	// svcAddrUpdateTimeout is the timeout setting for a goroutine to wait for the update of a service entry.
	// If the update process cannot be done within the timeout window, consider it failed.
	svcAddrUpdateTimeoutStr := os.Getenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT")
	svcAddrUpdateTimeout, err := time.ParseDuration(os.Getenv("ROUTER_SVC_ADDRESS_UPDATE_TIMEOUT"))
	if err != nil {
		svcAddrUpdateTimeout = 30 * time.Second
		logger.Error("failed to parse service address update timeout duration from 'ROUTER_ROUND_TRIP_SVC_ADDRESS_UPDATE_TIMEOUT' - set to the default value",
			zap.Error(err),
			zap.String("value", svcAddrUpdateTimeoutStr),
			zap.Duration("default", svcAddrUpdateTimeout))
	}

	// unTapServiceTimeout is the timeout used as timeout in the request context of unTapService
	unTapServiceTimeoutstr := os.Getenv("ROUTER_UNTAP_SERVICE_TIMEOUT")
	unTapServiceTimeout, err := time.ParseDuration(unTapServiceTimeoutstr)
	if err != nil {
		unTapServiceTimeout = 3600 * time.Second
		logger.Error("failed to parse unTap service timeout duration from 'ROUTER_UNTAP_SERVICE_TIMEOUT' - set to the default value",
			zap.Error(err),
			zap.String("value", unTapServiceTimeoutstr),
			zap.Duration("default", unTapServiceTimeout))
	}

	displayAccessLogStr := os.Getenv("DISPLAY_ACCESS_LOG")
	displayAccessLog, err := strconv.ParseBool(displayAccessLogStr)
	if err != nil {
		displayAccessLog = false
		logger.Error("failed to parse 'DISPLAY_ACCESS_LOG' - set to the default value",
			zap.Error(err),
			zap.String("value", displayAccessLogStr),
			zap.Bool("default", displayAccessLog))
	}

	triggers, err := makeHTTPTriggerSet(logger.Named("triggerset"), fmap, fissionClient, kubeClient, executor, &tsRoundTripperParams{
		timeout:           timeout,
		timeoutExponent:   timeoutExponent,
		disableKeepAlive:  disableKeepAlive,
		keepAliveTime:     keepAliveTime,
		maxRetries:        maxRetries,
		svcAddrRetryCount: svcAddrRetryCount,
	}, isDebugEnv, unTapServiceTimeout, throttler.MakeThrottler(svcAddrUpdateTimeout))
	if err != nil {
		return errors.Wrap(err, "error making HTTP trigger set")
	}

	mgr.Add(ctx, func(ctx context.Context) {
		metrics.ServeMetrics(ctx, "router", logger, mgr)
	})

	logger.Info("starting router", zap.Int("port", port))

	tracer := otel.Tracer("router")
	ctx, span := tracer.Start(ctx, "router/Start")
	defer span.End()

	return serve(ctx, logger, mgr, port, triggers, displayAccessLog)
}
