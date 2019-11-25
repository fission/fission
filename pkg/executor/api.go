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
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"go.opencensus.io/plugin/ochttp"
	"go.uber.org/zap"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/client"
)

func (executor *Executor) getServiceForFunctionApi(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}

	// get function metadata
	m := metav1.ObjectMeta{}
	err = json.Unmarshal(body, &m)
	if err != nil {
		http.Error(w, "Failed to parse request", http.StatusBadRequest)
		return
	}

	fn, err := executor.fissionClient.Functions(m.Namespace).Get(m.Name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "Failed to find function", http.StatusNotFound)
		} else {
			http.Error(w, "Failed to get function", http.StatusInternalServerError)
		}
		return
	}

	serviceName, err := executor.getServiceForFunction(fn)
	if err != nil {
		code, msg := ferror.GetHTTPError(err)
		executor.logger.Error("error getting service for function",
			zap.Error(err),
			zap.String("function", m.Name),
			zap.String("fission_http_error", msg))
		http.Error(w, msg, code)
		return
	}

	w.Write([]byte(serviceName))
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
	// Check function -> svc cache
	executor.logger.Debug("checking for cached function service",
		zap.String("function_name", fn.Metadata.Name),
		zap.String("function_namespace", fn.Metadata.Namespace))

	fsvc, err := executor.fsCache.GetByFunction(&fn.Metadata)
	if err == nil {
		if executor.isValidAddress(fsvc) {
			// Cached, return svc address
			return fsvc.Address, nil
		} else {
			executor.logger.Debug("deleting cache entry for invalid address",
				zap.String("function_name", fn.Metadata.Name),
				zap.String("function_namespace", fn.Metadata.Namespace),
				zap.String("address", fsvc.Address))
			executor.fsCache.DeleteEntry(fsvc)
		}
	}

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
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		executor.logger.Error("failed to read tap service request", zap.Error(err))
		http.Error(w, "Failed to read request", http.StatusInternalServerError)
		return
	}
	svcName := string(body)
	svcHost := strings.TrimPrefix(svcName, "http://")

	err = executor.fsCache.TouchByAddress(svcHost)
	if err != nil {
		executor.logger.Error("error tapping function service",
			zap.Error(err),
			zap.String("service", svcName),
			zap.String("host", svcHost))
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
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
		svcHost := strings.TrimPrefix(req.ServiceUrl, "http://")
		err = executor.fsCache.TouchByAddress(svcHost)
		if err != nil {
			errs = multierror.Append(errs,
				errors.Wrapf(err, "'%v' failed to tap function '%v/%v' with service url '%v'",
					req.FnMetadata.Namespace, req.FnMetadata.Name, req.ServiceUrl, req.FnExecutorType))
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

func (executor *Executor) GetHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/v2/getServiceForFunction", executor.getServiceForFunctionApi).Methods("POST")
	r.HandleFunc("/v2/tapService", executor.tapService).Methods("POST") // for backward compatibility
	r.HandleFunc("/v2/tapServices", executor.tapServices).Methods("POST")
	r.HandleFunc("/healthz", executor.healthHandler).Methods("GET")
	return r
}

func (executor *Executor) Serve(port int) {
	executor.logger.Info("starting executor", zap.Int("port", port))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	executor.ndm.Run(ctx)
	executor.gpm.Run(ctx)
	executor.cms.Run(ctx)

	address := fmt.Sprintf(":%v", port)
	err := http.ListenAndServe(address, &ochttp.Handler{
		Handler: executor.GetHandler(),
	})
	executor.logger.Fatal("done listening", zap.Error(err))
}
