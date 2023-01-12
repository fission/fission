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
	"container/list"
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dchest/uniuri"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/cms"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/executortype/container"
	"github.com/fission/fission/pkg/executor/executortype/newdeploy"
	"github.com/fission/fission/pkg/executor/executortype/poolmgr"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	// Executor defines a fission function executor.
	Executor struct {
		logger *zap.Logger

		executorTypes map[fv1.ExecutorType]executortype.ExecutorType
		cms           *cms.ConfigSecretController

		fissionClient versioned.Interface

		requestChan  chan *createFuncServiceRequest
		fnSvcReqChan chan *fnServiceReq
		fsCreateWg   sync.Map
		httpReqQueue *list.List
	}

	createFuncServiceRequest struct {
		context  context.Context
		function *fv1.Function
		respChan chan *createFuncServiceResponse
	}

	createFuncServiceResponse struct {
		funcSvc *fscache.FuncSvc
		err     error
	}

	fnServiceReq struct {
		fn       *fv1.Function
		context  context.Context
		respChan chan *fnServiceRespChan
	}

	fnServiceRespChan struct {
		svcAddress string
		err        *Err
	}

	Err struct {
		msg  string
		code int
	}
)

// MakeExecutor returns an Executor for given ExecutorType(s).
func MakeExecutor(ctx context.Context, logger *zap.Logger, cms *cms.ConfigSecretController,
	fissionClient versioned.Interface, types map[fv1.ExecutorType]executortype.ExecutorType,
	informers ...k8sCache.SharedIndexInformer) (*Executor, error) {
	executor := &Executor{
		logger:        logger.Named("executor"),
		cms:           cms,
		fissionClient: fissionClient,
		executorTypes: types,

		requestChan:  make(chan *createFuncServiceRequest),
		fnSvcReqChan: make(chan *fnServiceReq),
		httpReqQueue: list.New(),
	}

	// Run all informers
	for _, informer := range informers {
		go informer.Run(ctx.Done())
	}

	for _, et := range types {
		go func(et executortype.ExecutorType) {
			et.Run(ctx)
		}(et)
	}

	go executor.serveCreateFuncServices()
	go executor.getSVCForFunctionAPI()

	// sh:=list.New()

	return executor, nil
}

func (executor *Executor) getSVCForFunctionAPI() {
	for {
		executor.logger.Debug("waiting for request to be received")
		req := <-executor.fnSvcReqChan
		executor.logger.Debug("request received on a channel")
		// select {
		// case svcReq := <-executor.httpReqChan:
		// 	//todo
		// 	executor.logger.Debug("svc request", zap.String("context", svcReq.request.Method))
		// 	// case <-executor.httpReqChan.
		// }
		var serviceFound bool
		t := req.fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
		et := executor.executorTypes[t]
		logger := otelUtils.LoggerWithTraceID(req.context, executor.logger)

		// Check function -> svc cache
		logger.Debug("checking for cached function service",
			zap.String("function_name", req.fn.ObjectMeta.Name),
			zap.String("function_namespace", req.fn.ObjectMeta.Namespace))
		if t == fv1.ExecutorTypePoolmgr && !req.fn.Spec.OnceOnly {
			concurrency := req.fn.Spec.Concurrency
			if concurrency == 0 {
				concurrency = 500
			}
			requestsPerpod := req.fn.Spec.RequestsPerPod
			if requestsPerpod == 0 {
				requestsPerpod = 1
			}
			fsvc, active, err := et.GetFuncSvcFromPoolCache(req.context, req.fn, requestsPerpod)
			// check if its a cache hit (check if there is already specialized function pod that can serve another request)
			if err == nil {
				// if a pod is already serving request then it already exists else validated
				logger.Debug("from cache", zap.Int("active", active))
				if et.IsValid(req.context, fsvc) {
					// Cached, return svc address
					logger.Debug("served from cache", zap.String("name", fsvc.Name), zap.String("address", fsvc.Address))
					serviceFound = true
					req.respChan <- &fnServiceRespChan{svcAddress: fsvc.Address, err: nil}
					// executor.writeResponse(w, fsvc.Address, req.fn.ObjectMeta.Name)
					// return
				} else {
					logger.Debug("deleting cache entry for invalid address",
						zap.String("function_name", req.fn.ObjectMeta.Name),
						zap.String("function_namespace", req.fn.ObjectMeta.Namespace),
						zap.String("address", fsvc.Address))
					et.DeleteFuncSvcFromCache(req.context, fsvc)
					active--
				}
			}

			if !serviceFound {
				if active >= concurrency {
					errMsg := fmt.Sprintf("max concurrency reached for %v. All %v instance are active", req.fn.ObjectMeta.Name, concurrency)
					logger.Error("error occurred", zap.String("error", errMsg))
					// http.Error(w, html.EscapeString(errMsg), http.StatusTooManyRequests)
					req.respChan <- &fnServiceRespChan{err: &Err{msg: errMsg, code: http.StatusTooManyRequests}}
					// return
				} else {
					serviceName, err := executor.getServiceForFunction(req.context, req.fn)
					if err != nil {
						code, msg := ferror.GetHTTPError(err)
						logger.Error("error getting service for function",
							zap.Error(err),
							zap.String("function", req.fn.ObjectMeta.Name),
							zap.String("fission_http_error", msg))
						req.respChan <- &fnServiceRespChan{err: &Err{msg: msg, code: code}}
						// http.Error(w, msg, code)
						// return
					} else {
						// executor.writeResponse(w, serviceName, req.fn.ObjectMeta.Name)
						req.respChan <- &fnServiceRespChan{svcAddress: serviceName, err: nil}
						// return
					}
				}
			}
		} else if t == fv1.ExecutorTypeNewdeploy || t == fv1.ExecutorTypeContainer {
			fsvc, err := et.GetFuncSvcFromCache(req.context, req.fn)
			if err == nil {
				if et.IsValid(req.context, fsvc) {
					// Cached, return svc address
					// executor.writeResponse(w, fsvc.Address, req.fn.ObjectMeta.Name)
					serviceFound = true
					req.respChan <- &fnServiceRespChan{svcAddress: fsvc.Address, err: nil}
					// return
				} else {
					logger.Debug("deleting cache entry for invalid address",
						zap.String("function_name", req.fn.ObjectMeta.Name),
						zap.String("function_namespace", req.fn.ObjectMeta.Namespace),
						zap.String("address", fsvc.Address))
					et.DeleteFuncSvcFromCache(req.context, fsvc)
				}
			}
			if !serviceFound {
				serviceName, err := executor.getServiceForFunction(req.context, req.fn)
				if err != nil {
					code, msg := ferror.GetHTTPError(err)
					logger.Error("error getting service for function",
						zap.Error(err),
						zap.String("function", req.fn.ObjectMeta.Name),
						zap.String("fission_http_error", msg))
					req.respChan <- &fnServiceRespChan{err: &Err{msg: msg, code: code}}
					// http.Error(w, msg, code)
					// return
				} else {
					// executor.writeResponse(w, serviceName, req.fn.ObjectMeta.Name)
					req.respChan <- &fnServiceRespChan{svcAddress: serviceName, err: nil}
					// return
				}

			}
		}

		// serviceName, err := executor.getServiceForFunction(req.context, req.fn)
		// if err != nil {
		// 	code, msg := ferror.GetHTTPError(err)
		// 	logger.Error("error getting service for function",
		// 		zap.Error(err),
		// 		zap.String("function", req.fn.ObjectMeta.Name),
		// 		zap.String("fission_http_error", msg))
		// 	req.respChan <- &fnServiceRespChan{err: &Err{msg: msg, code: code}}
		// 	// http.Error(w, msg, code)
		// 	// return
		// } else {
		// 	// executor.writeResponse(w, serviceName, req.fn.ObjectMeta.Name)
		// 	req.respChan <- &fnServiceRespChan{svcAddress: serviceName, err: nil}
		// 	// return
		// }

	}
}

// func (executor *Executor) getFunctionAPI()

// All non-cached function service requests go through this goroutine
// serially. It parallelizes requests for different functions, and
// ensures that for a given function, only one request causes a pod to
// get specialized. In other words, it ensures that when there's an
// ongoing request for a certain function, all other requests wait for
// that request to complete.
func (executor *Executor) serveCreateFuncServices() {
	for {
		req := <-executor.requestChan
		fnMetadata := &req.function.ObjectMeta

		if req.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
			go func() {
				buffer := 10 // add some buffer time for specialization
				specializationTimeout := req.function.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout

				// set minimum specialization timeout to avoid illegal input and
				// compatibility problem when applying old spec file that doesn't
				// have specialization timeout field.
				if specializationTimeout < fv1.DefaultSpecializationTimeOut {
					specializationTimeout = fv1.DefaultSpecializationTimeOut
				}

				fnSpecializationTimeoutContext, cancel := context.WithTimeout(req.context,
					time.Duration(specializationTimeout+buffer)*time.Second)
				defer cancel()

				fsvc, err := executor.createServiceForFunction(fnSpecializationTimeoutContext, req.function)
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
					err:     err,
				}
			}()
			continue
		}

		// Cache miss -- is this first one to request the func?
		wg, found := executor.fsCreateWg.Load(crd.CacheKey(fnMetadata))
		if !found {
			// create a waitgroup for other requests for
			// the same function to wait on
			wg := &sync.WaitGroup{}
			wg.Add(1)
			executor.fsCreateWg.Store(crd.CacheKey(fnMetadata), wg)

			// launch a goroutine for each request, to parallelize
			// the specialization of different functions
			go func() {
				// Control overall specialization time by setting function
				// specialization time to context. The reason not to use
				// context from router requests is because a request maybe
				// canceled for unknown reasons and let executor keeps
				// spawning pods that never finish specialization process.
				// Also, even a request failed, a specialized function pod
				// still can serve other subsequent requests.

				buffer := 10 // add some buffer time for specialization
				specializationTimeout := req.function.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout

				// set minimum specialization timeout to avoid illegal input and
				// compatibility problem when applying old spec file that doesn't
				// have specialization timeout field.
				if specializationTimeout < fv1.DefaultSpecializationTimeOut {
					specializationTimeout = fv1.DefaultSpecializationTimeOut
				}

				fnSpecializationTimeoutContext, cancel := context.WithTimeout(req.context,
					time.Duration(specializationTimeout+buffer)*time.Second)
				defer cancel()

				fsvc, err := executor.createServiceForFunction(fnSpecializationTimeoutContext, req.function)
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
					err:     err,
				}
				executor.fsCreateWg.Delete(crd.CacheKey(fnMetadata))
				wg.Done()
			}()
		} else {
			// There's an existing request for this function, wait for it to finish
			go func() {
				executor.logger.Debug("waiting for concurrent request for the same function",
					zap.Any("function", fnMetadata))
				wg, ok := wg.(*sync.WaitGroup)
				if !ok {
					err := fmt.Errorf("could not convert value to workgroup for function %v in namespace %v", fnMetadata.Name, fnMetadata.Namespace)
					req.respChan <- &createFuncServiceResponse{
						funcSvc: nil,
						err:     err,
					}
				}
				wg.Wait()

				// get the function service from the cache
				fsvc, err := executor.getFunctionServiceFromCache(req.context, req.function)

				// fsCache return error when the entry does not exist/expire.
				// It normally happened if there are multiple requests are
				// waiting for the same function and executor failed to cre-
				// ate service for function.
				err = errors.Wrapf(err, "error getting service for function %v in namespace %v", fnMetadata.Name, fnMetadata.Namespace)
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
					err:     err,
				}
			}()
		}
	}
}

func (executor *Executor) createServiceForFunction(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, executor.logger)
	otelUtils.SpanTrackEvent(ctx, "createServiceForFunction", otelUtils.GetAttributesForFunction(fn)...)
	logger.Debug("no cached function service found, creating one",
		zap.String("function_name", fn.ObjectMeta.Name),
		zap.String("function_namespace", fn.ObjectMeta.Namespace))

	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	e, ok := executor.executorTypes[t]
	if !ok {
		return nil, errors.Errorf("Unknown executor type '%v'", t)
	}

	fsvc, fsvcErr := e.GetFuncSvc(ctx, fn)
	if fsvcErr != nil {
		e := "error creating service for function"
		logger.Error(e,
			zap.Error(fsvcErr),
			zap.String("function_name", fn.ObjectMeta.Name),
			zap.String("function_namespace", fn.ObjectMeta.Namespace))
		fsvcErr = errors.Wrap(fsvcErr, fmt.Sprintf("[%s] %s", fn.ObjectMeta.Name, e))
	}

	return fsvc, fsvcErr
}

func (executor *Executor) getFunctionServiceFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	otelUtils.SpanTrackEvent(ctx, "getFunctionServiceFromCache", otelUtils.GetAttributesForFunction(fn)...)
	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	e, ok := executor.executorTypes[t]
	if !ok {
		return nil, errors.Errorf("Unknown executor type '%v'", t)
	}
	return e.GetFuncSvcFromCache(ctx, fn)
}

// StartExecutor Starts executor and the executor components such as Poolmgr,
// deploymgr and potential future executor types
func StartExecutor(ctx context.Context, logger *zap.Logger, port int) error {
	clientGen := crd.NewClientGenerator()
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return errors.Wrap(err, "error making the fission client")
	}
	kubernetesClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return errors.Wrap(err, "error making the kube client")
	}
	metricsClient, err := clientGen.GetMetricsClient()
	if err != nil {
		logger.Error("error making the metrics client", zap.Error(err))
	}

	err = crd.WaitForCRDs(ctx, logger, fissionClient)
	if err != nil {
		return errors.Wrap(err, "error waiting for CRDs")
	}

	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	if err != nil {
		return errors.Wrap(err, "Error making fetcher config")
	}

	executorInstanceID := strings.ToLower(uniuri.NewLen(8))

	podSpecPatch, err := util.GetSpecFromConfigMap(fv1.RuntimePodSpecPath)
	if err != nil {
		logger.Warn("error reading data for pod spec patch", zap.String("path", fv1.RuntimePodSpecPath), zap.Error(err))
	}

	logger.Info("Starting executor", zap.String("instanceID", executorInstanceID))

	finformerFactory := make(map[string]genInformer.SharedInformerFactory, 0)
	for _, ns := range utils.DefaultNSResolver().FissionResourceNS {
		finformerFactory[ns] = genInformer.NewFilteredSharedInformerFactory(fissionClient, time.Minute*30, ns, nil)
	}

	executorLabel, err := utils.GetInformerLabelByExecutor(fv1.ExecutorTypePoolmgr)
	if err != nil {
		return err
	}
	gpmInformerFactory := utils.GetInformerFactoryByExecutor(kubernetesClient, executorLabel, time.Minute*30)
	gpm, err := poolmgr.MakeGenericPoolManager(ctx,
		logger,
		fissionClient, kubernetesClient, metricsClient,
		fetcherConfig, executorInstanceID,
		finformerFactory,
		gpmInformerFactory, podSpecPatch)
	if err != nil {
		return errors.Wrap(err, "pool manager creation failed")
	}

	executorLabel, err = utils.GetInformerLabelByExecutor(fv1.ExecutorTypeNewdeploy)
	if err != nil {
		return err
	}
	ndmInformerFactory := utils.GetInformerFactoryByExecutor(kubernetesClient, executorLabel, time.Minute*30)
	ndm, err := newdeploy.MakeNewDeploy(ctx,
		logger,
		fissionClient, kubernetesClient,
		fetcherConfig, executorInstanceID,
		finformerFactory,
		ndmInformerFactory, podSpecPatch)
	if err != nil {
		return errors.Wrap(err, "new deploy manager creation failed")
	}

	executorLabel, err = utils.GetInformerLabelByExecutor(fv1.ExecutorTypeContainer)
	if err != nil {
		return err
	}
	cnmInformerFactory := utils.GetInformerFactoryByExecutor(kubernetesClient, executorLabel, time.Minute*30)
	cnm, err := container.MakeContainer(
		ctx, logger,
		fissionClient, kubernetesClient,
		executorInstanceID, finformerFactory,
		cnmInformerFactory)
	if err != nil {
		return errors.Wrap(err, "container manager creation failed")
	}

	executorTypes := make(map[fv1.ExecutorType]executortype.ExecutorType)
	executorTypes[gpm.GetTypeName(ctx)] = gpm
	executorTypes[ndm.GetTypeName(ctx)] = ndm
	executorTypes[cnm.GetTypeName(ctx)] = cnm

	adoptExistingResources, _ := strconv.ParseBool(os.Getenv("ADOPT_EXISTING_RESOURCES"))

	wg := &sync.WaitGroup{}
	for _, et := range executorTypes {
		wg.Add(1)
		go func(et executortype.ExecutorType) {
			defer wg.Done()
			if adoptExistingResources {
				et.AdoptExistingResources(ctx)
			}
			et.CleanupOldExecutorObjects(ctx)
		}(et)
	}
	// set hard timeout for resource adoption
	// TODO: use context to control the waiting time once kubernetes client supports it.
	util.WaitTimeout(wg, 30*time.Second)

	configMapInformer := utils.GetK8sInformersForNamespaces(kubernetesClient, time.Minute*30, fv1.ConfigMaps)
	secretInformer := utils.GetK8sInformersForNamespaces(kubernetesClient, time.Minute*30, fv1.Secrets)
	cms := cms.MakeConfigSecretController(ctx, logger, fissionClient, kubernetesClient, executorTypes, configMapInformer, secretInformer)

	fissionInformers := make([]k8sCache.SharedIndexInformer, 0)
	for _, informer := range configMapInformer {
		fissionInformers = append(fissionInformers, informer)
	}
	for _, informer := range secretInformer {
		fissionInformers = append(fissionInformers, informer)
	}
	for _, factory := range finformerFactory {
		factory.Start(ctx.Done())
	}
	for _, informerFactory := range gpmInformerFactory {
		informerFactory.Start(ctx.Done())
	}
	for _, informerFactory := range ndmInformerFactory {
		informerFactory.Start(ctx.Done())
	}
	for _, informerFactory := range cnmInformerFactory {
		informerFactory.Start(ctx.Done())
	}

	api, err := MakeExecutor(ctx, logger, cms, fissionClient, executorTypes,
		fissionInformers...,
	)
	if err != nil {
		return err
	}

	utils.CreateMissingPermissionForSA(ctx, kubernetesClient, logger)

	go metrics.ServeMetrics(ctx, logger)
	go api.Serve(ctx, port)

	return nil
}
