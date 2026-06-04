// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
	"golang.org/x/sync/errgroup"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

func (executor *Executor) getServiceForFunctionAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}

	// get function metadata
	fn := &fv1.Function{}
	err = json.Unmarshal(body, &fn)
	if err != nil {
		http.Error(w, "Failed to parse request", http.StatusBadRequest)
		return
	}

	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	et := executor.executorTypes[t]
	logger := otelUtils.LoggerWithTraceID(ctx, executor.logger)

	// Check function -> svc cache
	logger.V(1).Info("checking for cached function service",
		"function_name", fn.Name,
		"function_namespace", fn.Namespace)
	if t == fv1.ExecutorTypePoolmgr && !fn.Spec.OnceOnly {
		fsvc, err := et.GetFuncSvcFromCache(ctx, fn)
		// check if its a cache hit (check if there is already specialized function pod that can serve another request)
		if err == nil {
			// if a pod is already serving request then it already exists else validated
			if et.IsValid(ctx, fsvc) {
				// Cached, return svc address
				logger.V(1).Info("served from cache", "name", fsvc.Name, "address", fsvc.Address)
				executor.writeResponse(w, fsvc.Address, fn.Name)
				return
			}
			logger.V(1).Info("deleting cache entry for invalid address",
				"function_name", fn.Name,
				"function_namespace", fn.Namespace,
				"address", fsvc.Address)
			et.DeleteFuncSvcFromCache(ctx, fsvc)
		} else {
			code, msg := ferror.GetHTTPError(err)
			if code == http.StatusNotFound {
				logger.V(1).Info("cache miss", "function_name", fn.Name)
			} else {
				logger.Error(err, "error getting service for function", "function_name", fn.Name)
				http.Error(w, msg, code)
				return
			}
		}

	} else if t == fv1.ExecutorTypeNewdeploy || t == fv1.ExecutorTypeContainer {
		fsvc, err := et.GetFuncSvcFromCache(ctx, fn)
		if err == nil {
			if et.IsValid(ctx, fsvc) {
				// Cached, return svc address
				executor.writeResponse(w, fsvc.Address, fn.Name)
				return
			}
			logger.V(1).Info("deleting cache entry for invalid address",
				"function_name", fn.Name,
				"function_namespace", fn.Namespace,
				"address", fsvc.Address)
			et.DeleteFuncSvcFromCache(ctx, fsvc)
		}
	}

	serviceName, err := executor.getServiceForFunction(ctx, fn)
	if err != nil {
		code, msg := ferror.GetHTTPError(err)
		logger.Error(err, "error getting service for function", "function", fn.Name,
			"fission_http_error", msg)
		http.Error(w, msg, code)
		return
	}
	executor.writeResponse(w, serviceName, fn.Name)
}

func (executor *Executor) writeResponse(w http.ResponseWriter, serviceName string, fnName string) {
	_, err := w.Write([]byte(serviceName))
	if err != nil {
		executor.logger.Error(err,
			"error writing HTTP response", "function", fnName,
		)
	}
}

// getServiceForFunction first checks if this function's service is cached, if yes, it validates the address.
// if it's a valid address, just returns it.
// else, invalidates its cache entry and makes a new request to create a service for this function and finally responds
// with new address or error.
//
// checking for the validity of the address causes a little more over-head than desired. but, it ensures that
// stale addresses are not returned to the router.
// To make it optimal, plan is to add an eager cache invalidator function that watches for pod deletion events and
// invalidates the cache entry if the pod address was cached.
func (executor *Executor) getServiceForFunction(ctx context.Context, fn *fv1.Function) (string, error) {
	respChan := make(chan *createFuncServiceResponse)
	executor.requestChan <- &createFuncServiceRequest{
		context:  ctx,
		function: fn,
		respChan: respChan,
	}
	resp := <-respChan
	cleanUp := func(funcSvc *fscache.FuncSvc) {
		et, ok := executor.executorTypes[fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType]
		if !ok {
			executor.logger.Info("unknown executor type received in function service", "executor", funcSvc.Executor)
			return
		}
		if funcSvc != nil {
			et.UnTapService(ctx, funcSvc.Function, resp.funcSvc.Address)
		} else {
			et.MarkSpecializationFailure(ctx, &fn.ObjectMeta)
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		cleanUp(resp.funcSvc)
		return "", ferror.MakeError(499, "client leave early in the process of getServiceForFunction")
	}
	if resp.err != nil {
		cleanUp(resp.funcSvc)
		return "", resp.err
	}
	return resp.funcSvc.Address, resp.err
}

// find funcSvc and update its atime
// TODO: Deprecated tapService
func (executor *Executor) tapService(w http.ResponseWriter, r *http.Request) {
	// only for upgrade compatibility
	w.WriteHeader(http.StatusOK)
}

// find funcSvc and update its atime
func (executor *Executor) tapServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := otelUtils.LoggerWithTraceID(ctx, executor.logger)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error(err, "failed to read tap service request")
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}

	tapSvcReqs := []client.TapServiceRequest{}
	err = json.Unmarshal(body, &tapSvcReqs)
	if err != nil {
		logger.Error(err, "failed to decode tap service request", "request-payload", string(body))
		http.Error(w, "Failed to decode tap service request", http.StatusBadRequest)
		return
	}

	var errs, notFound error
	for _, req := range tapSvcReqs {
		svcHost := strings.TrimPrefix(req.ServiceURL, "http://")

		et, exists := executor.executorTypes[req.FnExecutorType]
		if !exists {
			errs = errors.Join(errs,
				fmt.Errorf("error tapping service due to unknown executor type '%s' found",
					req.FnExecutorType))
			continue
		}

		if err := et.TapService(ctx, svcHost); err != nil {
			wrapped := fmt.Errorf("error tapping function '%s/%s' with executor '%s' and service url '%s': %w", req.FnMetadata.Namespace, req.FnMetadata.Name, req.FnExecutorType, req.ServiceURL, err)
			if ferror.IsNotFound(err) {
				notFound = errors.Join(notFound, wrapped)
			} else {
				errs = errors.Join(errs, wrapped)
			}
		}
	}

	if errs != nil {
		logger.Error(errs, "error tapping function service")
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if notFound != nil {
		// Expired/deleted fsvcs are routine churn — the router's entry is
		// stale, not broken. Still 404 so the caller knows, but not an
		// error-level log.
		logger.V(1).Info("tap skipped for expired function services", "detail", notFound.Error())
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (executor *Executor) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// readyzHandler reports readiness for the executor Service. It returns 200 only
// when this replica is the leader (or leader election is disabled) AND its
// informer caches have synced. Non-leaders report 503 so the kubelet keeps
// them out of the Service endpoints, routing all traffic to the leader; the
// standby is ready to take over the moment it wins the lease. /healthz stays a
// cheap liveness check.
func (executor *Executor) readyzHandler(w http.ResponseWriter, r *http.Request) {
	if executor.leaderElection && !executor.isLeader.Load() {
		http.Error(w, "not leader", http.StatusServiceUnavailable)
		return
	}
	if !executor.cachesSynced.Load() {
		http.Error(w, "caches not synced", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (executor *Executor) unTapService(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}
	tapSvcReq := client.TapServiceRequest{}
	err = json.Unmarshal(body, &tapSvcReq)
	if err != nil {
		http.Error(w, "Failed to parse request", http.StatusBadRequest)
		return
	}
	t := tapSvcReq.FnExecutorType
	if t != fv1.ExecutorTypePoolmgr {
		msg := fmt.Sprintf("Unknown executor type '%s'", t)
		http.Error(w, html.EscapeString(msg), http.StatusBadRequest)
		return
	}

	et := executor.executorTypes[t]

	et.UnTapService(ctx, &tapSvcReq.FnMetadata, tapSvcReq.ServiceURL)

	w.WriteHeader(http.StatusOK)
}

// dumpDebugInfo => dump function service for pool cache
func (executor *Executor) dumpDebugInfo(w http.ResponseWriter, r *http.Request) {
	// currently we are considering dumping function only for pool manager
	et := executor.executorTypes[fv1.ExecutorTypePoolmgr]
	if err := et.DumpDebugInfo(r.Context()); err != nil {
		code, msg := ferror.GetHTTPError(err)
		http.Error(w, msg, code)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// GetHandler returns an http.Handler.
func (executor *Executor) GetHandler() http.Handler {
	r := mux.NewRouter()
	// Register the HMAC verifier middleware before the metrics
	// middleware so 401 rejections are counted at the same layer as
	// other auth failures and the metrics middleware sees the post-
	// verification request. The master secret (when set via
	// FISSION_INTERNAL_AUTH_SECRET on the executor pod) is derived
	// per-service for ServiceExecutor so a leak of this executor's
	// runtime memory cannot forge requests on other Fission internal
	// channels (storagesvc, fetcher, builder, router-internal). An
	// empty master means the underlying Verifier short-circuits to
	// pass-through, preserving backwards compatibility for installs
	// with internalAuth disabled. /healthz is bypassed so kubelet
	// probes continue to pass without signing. See
	// docs/internal-auth/00-design.md.
	master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	masterOld := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD"))
	r.Use(hmacauth.ServiceVerifier(master, masterOld, hmacauth.ServiceExecutor, hmacauth.VerifierOpts{
		SkewSec:      60,
		Bypass:       []string{"/healthz", "/readyz"},
		MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
		Logger:       executor.logger.WithName("hmac"),
	}))
	r.Use(metrics.HTTPMetricMiddleware)
	r.HandleFunc("/v2/getServiceForFunction", executor.getServiceForFunctionAPI).Methods("POST")
	r.HandleFunc("/v2/tapService", executor.tapService).Methods("POST") // for backward compatibility
	r.HandleFunc("/v2/tapServices", executor.tapServices).Methods("POST")
	r.HandleFunc("/healthz", executor.healthHandler).Methods("GET")
	r.HandleFunc("/readyz", executor.readyzHandler).Methods("GET")
	r.HandleFunc("/v2/unTapService", executor.unTapService).Methods("POST")
	r.HandleFunc("/v2/debugInfo", executor.dumpDebugInfo).Methods("GET")
	return r
}

// Serve starts an HTTP server.
//
// The handler chain is, from inside out: GetHandler (HMAC verifier +
// metrics + business handlers) → otel → DenyAllCORS → SecurityHeaders.
// Executor has no legitimate browser caller (router-only per
// charts/fission-all/templates/executor/networkpolicy.yaml); the CORS
// deny is defense-in-depth if a future regression exposes this port via
// Ingress.
func (executor *Executor) Serve(ctx context.Context, mgr *errgroup.Group, port int) {
	handler := httpsecurity.SecurityHeaders(
		httpsecurity.DenyAllCORS(
			otelUtils.GetHandlerWithOTEL(executor.GetHandler(), "fission-executor", otelUtils.UrlsToIgnore("/healthz")),
		),
	)
	httpserver.StartServer(ctx, executor.logger, mgr, "executor", fmt.Sprintf("%d", port), handler)
}
