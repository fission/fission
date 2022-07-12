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

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"go.uber.org/zap"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/client"
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
	logger.Debug("checking for cached function service",
		zap.String("function_name", fn.ObjectMeta.Name),
		zap.String("function_namespace", fn.ObjectMeta.Namespace))
	if t == fv1.ExecutorTypePoolmgr && !fn.Spec.OnceOnly {
		concurrency := fn.Spec.Concurrency
		if concurrency == 0 {
			concurrency = 500
		}
		requestsPerpod := fn.Spec.RequestsPerPod
		if requestsPerpod == 0 {
			requestsPerpod = 1
		}
		fsvc, active, err := et.GetFuncSvcFromPoolCache(ctx, fn, requestsPerpod)
		// check if its a cache hit (check if there is already specialized function pod that can serve another request)
		if err == nil {
			// if a pod is already serving request then it already exists else validated
			logger.Debug("from cache", zap.Int("active", active))
			if et.IsValid(ctx, fsvc) {
				// Cached, return svc address
				logger.Debug("served from cache", zap.String("name", fsvc.Name), zap.String("address", fsvc.Address))
				executor.writeResponse(w, fsvc.Address, fn.ObjectMeta.Name)
				return
			}
			logger.Debug("deleting cache entry for invalid address",
				zap.String("function_name", fn.ObjectMeta.Name),
				zap.String("function_namespace", fn.ObjectMeta.Namespace),
				zap.String("address", fsvc.Address))
			et.DeleteFuncSvcFromCache(ctx, fsvc)
			active--
		}

		if active >= concurrency {
			errMsg := fmt.Sprintf("max concurrency reached for %v. All %v instance are active", fn.ObjectMeta.Name, concurrency)
			logger.Error("error occurred", zap.String("error", errMsg))
			http.Error(w, html.EscapeString(errMsg), http.StatusTooManyRequests)
			return
		}
	} else if t == fv1.ExecutorTypeNewdeploy || t == fv1.ExecutorTypeContainer {
		fsvc, err := et.GetFuncSvcFromCache(ctx, fn)
		if err == nil {
			if et.IsValid(ctx, fsvc) {
				// Cached, return svc address
				executor.writeResponse(w, fsvc.Address, fn.ObjectMeta.Name)
				return
			}
			logger.Debug("deleting cache entry for invalid address",
				zap.String("function_name", fn.ObjectMeta.Name),
				zap.String("function_namespace", fn.ObjectMeta.Namespace),
				zap.String("address", fsvc.Address))
			et.DeleteFuncSvcFromCache(ctx, fsvc)
		}
	}

	serviceName, err := executor.getServiceForFunction(ctx, fn)
	if err != nil {
		code, msg := ferror.GetHTTPError(err)
		logger.Error("error getting service for function",
			zap.Error(err),
			zap.String("function", fn.ObjectMeta.Name),
			zap.String("fission_http_error", msg))
		http.Error(w, msg, code)
		return
	}
	executor.writeResponse(w, serviceName, fn.ObjectMeta.Name)
}

func (executor *Executor) writeResponse(w http.ResponseWriter, serviceName string, fnName string) {
	_, err := w.Write([]byte(serviceName))
	if err != nil {
		executor.logger.Error(
			"error writing HTTP response",
			zap.String("function", fnName),
			zap.Error(err),
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
	if resp.err != nil {
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
		logger.Error("failed to read tap service request", zap.Error(err))
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}

	tapSvcReqs := []client.TapServiceRequest{}
	err = json.Unmarshal(body, &tapSvcReqs)
	if err != nil {
		logger.Error("failed to decode tap service request",
			zap.Error(err),
			zap.String("request-payload", string(body)))
		http.Error(w, "Failed to decode tap service request", http.StatusBadRequest)
		return
	}

	errs := &multierror.Error{}
	for _, req := range tapSvcReqs {
		svcHost := strings.TrimPrefix(req.ServiceURL, "http://")

		et, exists := executor.executorTypes[req.FnExecutorType]
		if !exists {
			errs = multierror.Append(errs,
				errors.Errorf("error tapping service due to unknown executor type '%v' found",
					req.FnExecutorType))
			continue
		}

		err = et.TapService(ctx, svcHost)
		if err != nil {
			errs = multierror.Append(errs,
				errors.Wrapf(err, "'%v' failed to tap function '%v' in '%v' with service url '%v'",
					req.FnMetadata.Name, req.FnMetadata.Namespace, req.ServiceURL, req.FnExecutorType))
		}
	}

	if errs.ErrorOrNil() != nil {
		logger.Error("error tapping function service", zap.Error(errs))
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (executor *Executor) healthHandler(w http.ResponseWriter, r *http.Request) {
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
	key := fmt.Sprintf("%v_%v", tapSvcReq.FnMetadata.UID, tapSvcReq.FnMetadata.ResourceVersion)
	t := tapSvcReq.FnExecutorType
	if t != fv1.ExecutorTypePoolmgr {
		msg := fmt.Sprintf("Unknown executor type '%v'", t)
		http.Error(w, html.EscapeString(msg), http.StatusBadRequest)
		return
	}

	et := executor.executorTypes[t]

	et.UnTapService(ctx, key, tapSvcReq.ServiceURL)

	w.WriteHeader(http.StatusOK)
}

// GetHandler returns an http.Handler.
func (executor *Executor) GetHandler() http.Handler {
	r := mux.NewRouter()
	r.Use(metrics.HTTPMetricMiddleware)
	r.HandleFunc("/v2/getServiceForFunction", executor.getServiceForFunctionAPI).Methods("POST")
	r.HandleFunc("/v2/tapService", executor.tapService).Methods("POST") // for backward compatibility
	r.HandleFunc("/v2/tapServices", executor.tapServices).Methods("POST")
	r.HandleFunc("/healthz", executor.healthHandler).Methods("GET")
	r.HandleFunc("/v2/unTapService", executor.unTapService).Methods("POST")
	return r
}

// Serve starts an HTTP server.
func (executor *Executor) Serve(ctx context.Context, port int) {
	handler := otelUtils.GetHandlerWithOTEL(executor.GetHandler(), "fission-executor", otelUtils.UrlsToIgnore("/healthz"))
	httpserver.StartServer(ctx, executor.logger, "executor", fmt.Sprintf("%d", port), handler)

}
