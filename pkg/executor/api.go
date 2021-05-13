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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"go.opencensus.io/plugin/ochttp"
	"go.uber.org/zap"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/client"
)

func (executor *Executor) getServiceForFunctionAPI(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
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

	// Check function -> svc cache
	executor.logger.Debug("checking for cached function service",
		zap.String("function_name", fn.ObjectMeta.Name),
		zap.String("function_namespace", fn.ObjectMeta.Namespace))
	if t == fv1.ExecutorTypePoolmgr {
		concurrency := fn.Spec.Concurrency
		if concurrency == 0 {
			concurrency = 500
		}
		requestsPerpod := fn.Spec.RequestsPerPod
		if requestsPerpod == 0 {
			requestsPerpod = 1
		}
		fsvc, active, err := et.GetFuncSvcFromPoolCache(fn, requestsPerpod)
		// check if its a cache hit (check if there is already specialized function pod that can serve another request)
		if err == nil {
			// if a pod is already serving request then it already exists else validated
			executor.logger.Debug("from cache", zap.Int("active", active))
			if active > 1 || et.IsValid(fsvc) {
				// Cached, return svc address
				executor.logger.Debug("served from cache", zap.String("name", fsvc.Name), zap.String("address", fsvc.Address))
				executor.writeResponse(w, fsvc.Address, fn.ObjectMeta.Name)
				return
			}
			executor.logger.Debug("deleting cache entry for invalid address",
				zap.String("function_name", fn.ObjectMeta.Name),
				zap.String("function_namespace", fn.ObjectMeta.Namespace),
				zap.String("address", fsvc.Address))
			et.DeleteFuncSvcFromCache(fsvc)
			active--
		}

		if active >= concurrency {
			errMsg := fmt.Sprintf("max concurrency reached for %v. All %v instance are active", fn.ObjectMeta.Name, concurrency)
			executor.logger.Error("error occurred", zap.String("error", errMsg))
			http.Error(w, errMsg, http.StatusTooManyRequests)
			return
		}
	} else {
		fsvc, err := et.GetFuncSvcFromCache(fn)
		if err == nil {
			if et.IsValid(fsvc) {
				// Cached, return svc address
				executor.writeResponse(w, fsvc.Address, fn.ObjectMeta.Name)
				return
			}
			executor.logger.Debug("deleting cache entry for invalid address",
				zap.String("function_name", fn.ObjectMeta.Name),
				zap.String("function_namespace", fn.ObjectMeta.Namespace),
				zap.String("address", fsvc.Address))
			et.DeleteFuncSvcFromCache(fsvc)
		}
	}

	serviceName, err := executor.getServiceForFunction(fn)
	if err != nil {
		code, msg := ferror.GetHTTPError(err)
		executor.logger.Error("error getting service for function",
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
func (executor *Executor) getServiceForFunction(fn *fv1.Function) (string, error) {
	respChan := make(chan *createFuncServiceResponse)
	executor.requestChan <- &createFuncServiceRequest{
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
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		executor.logger.Error("failed to read tap service request", zap.Error(err))
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}

	tapSvcReqs := []client.TapServiceRequest{}
	err = json.Unmarshal(body, &tapSvcReqs)
	if err != nil {
		executor.logger.Error("failed to decode tap service request",
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

		err = et.TapService(svcHost)
		if err != nil {
			errs = multierror.Append(errs,
				errors.Wrapf(err, "'%v' failed to tap function '%v' in '%v' with service url '%v'",
					req.FnMetadata.Name, req.FnMetadata.Namespace, req.ServiceURL, req.FnExecutorType))
		}
	}

	if errs.ErrorOrNil() != nil {
		executor.logger.Error("error tapping function service", zap.Error(errs))
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (executor *Executor) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (executor *Executor) unTapService(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
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
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	et := executor.executorTypes[t]

	et.UnTapService(key, tapSvcReq.ServiceURL)

	w.WriteHeader(http.StatusOK)
}

// GetHandler returns an http.Handler.
func (executor *Executor) GetHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/v2/getServiceForFunction", executor.getServiceForFunctionAPI).Methods("POST")
	r.HandleFunc("/v2/tapService", executor.tapService).Methods("POST") // for backward compatibility
	r.HandleFunc("/v2/tapServices", executor.tapServices).Methods("POST")
	r.HandleFunc("/healthz", executor.healthHandler).Methods("GET")
	r.HandleFunc("/v2/unTapService", executor.unTapService).Methods("POST")
	return r
}

// Serve starts an HTTP server.
func (executor *Executor) Serve(port int) {
	executor.logger.Info("starting executor API", zap.Int("port", port))
	address := fmt.Sprintf(":%v", port)
	err := http.ListenAndServe(address, &ochttp.Handler{
		Handler: executor.GetHandler(),
	})
	executor.logger.Fatal("done listening", zap.Error(err))
}
