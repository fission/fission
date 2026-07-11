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
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/utils/correlation"
	"github.com/fission/fission/pkg/utils/httpmux"
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
		// failOnCacheError: a non-NotFound cache error for poolmgr means the
		// concurrency gate itself failed (e.g. 429 at the concurrency cap), so
		// it must be relayed instead of falling through to specialization.
		if executor.serveFromCache(ctx, w, et, fn, true) {
			return
		}
	} else if t == fv1.ExecutorTypeNewdeploy || t == fv1.ExecutorTypeContainer {
		if executor.serveFromCache(ctx, w, et, fn, false) {
			return
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

// serveFromCache checks the function→service cache and writes the cached
// address when a valid entry exists (a specialized pod that can serve another
// request). It returns true when a response has been written — a cache hit, or
// (with failOnCacheError) a fatal cache error; false means the caller should
// proceed to create a new service. Invalid entries are evicted on the way.
func (executor *Executor) serveFromCache(ctx context.Context, w http.ResponseWriter, et executortype.ExecutorType, fn *fv1.Function, failOnCacheError bool) bool {
	logger := otelUtils.LoggerWithTraceID(ctx, executor.logger)

	fsvc, err := et.GetFuncSvcFromCache(ctx, fn)
	if err != nil {
		if !failOnCacheError {
			return false
		}
		code, msg := ferror.GetHTTPError(err)
		if code == http.StatusNotFound {
			logger.V(1).Info("cache miss", "function_name", fn.Name)
			return false
		}
		logger.Error(err, "error getting service for function", "function_name", fn.Name)
		http.Error(w, msg, code)
		return true
	}
	// if a pod is already serving request then it already exists else validated
	if et.IsValid(ctx, fsvc) {
		// Cached, return svc address
		logger.V(1).Info("served from cache", "name", fsvc.Name, "address", fsvc.Address)
		executor.writeResponse(w, fsvc.Address, fn.Name)
		return true
	}
	logger.V(1).Info("deleting cache entry for invalid address",
		"function_name", fn.Name,
		"function_namespace", fn.Namespace,
		"address", fsvc.Address)
	et.DeleteFuncSvcFromCache(ctx, fsvc)
	return false
}

func (executor *Executor) writeResponse(w http.ResponseWriter, serviceName string, fnName string) {
	_, err := w.Write([]byte(serviceName))
	if err != nil {
		executor.logger.Error(err,
			"error writing HTTP response", "function", fnName,
		)
	}
}

// getServiceForFunction dispatches the creation of a service for the function
// (deduplicated per function for newdeploy/container, independent runs for
// poolmgr — see dispatchCreateFuncService) and returns its address.
func (executor *Executor) getServiceForFunction(ctx context.Context, fn *fv1.Function) (string, error) {
	// Specializations are leader-only work: with the old request-channel
	// multiplexer the consumer ran in the leader-elected runnable and a
	// non-leader's request simply never progressed. Refuse explicitly instead —
	// /readyz keeps non-leaders out of the Service, so this only triggers on
	// direct hits during failover windows, where specializing would create
	// pools/pods that fight the leader's instanceID-based reaper.
	if executor.leaderElection && !executor.isLeader.Load() {
		return "", ferror.MakeError(http.StatusServiceUnavailable, "not the leader; retry against the executor service")
	}
	fsvc, err := executor.dispatchCreateFuncService(ctx, fn)
	cleanUp := func(funcSvc *fscache.FuncSvc) {
		et, ok := executor.executorTypes[fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType]
		if !ok {
			executor.logger.Info("unknown executor type received in function service", "executor", fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)
			return
		}
		if funcSvc != nil {
			// The pod was allotted but the caller is gone / errored — release it.
			et.UnTapService(ctx, funcSvc.Function, funcSvc.Address)
		} else {
			et.MarkSpecializationFailure(ctx, &fn.ObjectMeta)
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		cleanUp(fsvc)
		return "", ferror.MakeError(499, "client leave early in the process of getServiceForFunction")
	}
	if err != nil {
		cleanUp(fsvc)
		return "", err
	}
	return fsvc.Address, nil
}

// capacityReserver is the optional executor-type facet ensureCapacity uses to
// enforce the function's concurrency cap: an atomic check-and-reserve inside
// the PoolCache (the capacity authority), so concurrent capacity requests
// cannot race past the cap. The reservation is consumed by the
// specialization's success (setValue) or failure (MarkSpecializationFailure)
// accounting.
type capacityReserver interface {
	ReserveCapacity(ctx context.Context, fnMeta *metav1.ObjectMeta, concurrency int) error
}

// ensureCapacityHandler serves POST /v2/ensureCapacity (RFC-0002): the router
// calls it when every endpoint it knows for a poolmgr function is saturated by
// its local admission accounting. The executor — still the capacity authority —
// either synchronously specializes one more pod (responding with its address,
// the same shape as getServiceForFunction) or answers 429 at the function's
// concurrency cap. Unlike getServiceForFunction it never serves from the
// PoolCache: the router only calls it because the cached pods are busy.
func (executor *Executor) ensureCapacityHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}

	var req client.EnsureCapacityRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Function == nil {
		http.Error(w, "Failed to parse request", http.StatusBadRequest)
		return
	}
	fn := req.Function
	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	if t != fv1.ExecutorTypePoolmgr {
		http.Error(w, fmt.Sprintf("ensureCapacity supports poolmgr only, got '%s'", html.EscapeString(string(t))), http.StatusBadRequest)
		return
	}
	et := executor.executorTypes[t]
	logger := otelUtils.LoggerWithTraceID(ctx, executor.logger)

	if cr, ok := et.(capacityReserver); ok {
		if err := cr.ReserveCapacity(ctx, &fn.ObjectMeta, fn.GetConcurrency()); err != nil {
			code, msg := ferror.GetHTTPError(err)
			logger.V(1).Info("ensureCapacity rejected at concurrency cap",
				"function_name", fn.Name, "function_namespace", fn.Namespace,
				"concurrency", fn.GetConcurrency(),
				"observed_ready", req.ObservedReadyEndpoints, "observed_busy", req.ObservedBusyEndpoints)
			http.Error(w, msg, code)
			return
		}
	}

	serviceName, err := executor.getServiceForFunction(ctx, fn)
	if err != nil {
		code, msg := ferror.GetHTTPError(err)
		logger.Error(err, "error ensuring capacity for function", "function", fn.Name,
			"fission_http_error", msg)
		http.Error(w, msg, code)
		return
	}
	executor.writeResponse(w, serviceName, fn.Name)
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
		// Genuine tap failures (unknown executor type, internal errors) must be
		// distinguishable on the wire from routine fsvc expiry (404 below): the
		// router's flush treats them identically otherwise, and with the
		// RFC-0002 warm path taps are the pods' only liveness signal.
		logger.Error(errs, "error tapping function service")
		http.Error(w, "error tapping function services", http.StatusInternalServerError)
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
	// The HMAC verifier wraps the whole mux as the OUTERMOST middleware so 401
	// rejections are handled at the auth layer and the per-route metrics see
	// only post-verification requests. The master secret (when set via
	// FISSION_INTERNAL_AUTH_SECRET on the executor pod) is derived per-service
	// for ServiceExecutor so a leak of this executor's runtime memory cannot
	// forge requests on other Fission internal channels (storagesvc, fetcher,
	// builder, router-internal). An empty master means the underlying Verifier
	// short-circuits to pass-through, preserving backwards compatibility for
	// installs with internalAuth disabled. /healthz and /readyz are bypassed so
	// kubelet probes continue to pass without signing. See
	// docs/internal-auth/00-design.md.
	master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	masterOld := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD"))
	verifier := hmacauth.ServiceVerifier(master, masterOld, hmacauth.ServiceExecutor, hmacauth.VerifierOpts{
		SkewSec:      60,
		Bypass:       []string{"/healthz", "/readyz"},
		MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
		Logger:       executor.logger.WithName("hmac"),
	})
	m := httpmux.New(
		httpmux.WithMiddleware(verifier),
		httpmux.WithMetrics(metrics.HTTPRecorder{}),
	)
	m.HandleFunc("/v2/getServiceForFunction", executor.getServiceForFunctionAPI).Methods("POST")
	m.HandleFunc("/v2/ensureCapacity", executor.ensureCapacityHandler).Methods("POST")
	m.HandleFunc("/v2/tapServices", executor.tapServices).Methods("POST")
	m.HandleFunc("/v2/unTapService", executor.unTapService).Methods("POST")
	m.HandleFunc("/v2/debugInfo", executor.dumpDebugInfo).Methods("GET")
	m.HandleFunc("/healthz", executor.healthHandler).Methods("GET")
	m.HandleFunc("/readyz", executor.readyzHandler).Methods("GET")
	return m.Handler()
}

// Serve starts an HTTP server.
//
// The handler chain is, from inside out: GetHandler (HMAC verifier +
// metrics + business handlers) → otel → DenyAllCORS → SecurityHeaders.
// Executor has no legitimate browser caller (router-only per
// charts/fission-all/templates/executor/networkpolicy.yaml); the CORS
// deny is defense-in-depth if a future regression exposes this port via
// Ingress.
func (executor *Executor) Serve(ctx context.Context, mgr *errgroup.Group, port int, listener net.Listener) {
	// correlation.Middleware (inside OTEL, outside the HMAC verifier which lives
	// in GetHandler) extracts the inbound X-Fission-Request-ID into the request
	// context so a cold-start specialization and its fetcher call carry the same
	// id as the router request that triggered them (RFC-0015). The verifier signs
	// method + URI + body only, so the header never affects the signature.
	handler := httpsecurity.SecurityHeaders(
		httpsecurity.DenyAllCORS(
			otelUtils.GetHandlerWithOTEL(correlation.Middleware(executor.GetHandler()), "fission-executor", otelUtils.UrlsToIgnore("/healthz")),
		),
	)
	httpserver.Serve(ctx, executor.logger, mgr, httpserver.ServerOptions{
		Name: "executor", Addr: strconv.Itoa(port), Listener: listener, Handler: handler,
	})
}
