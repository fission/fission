// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dchest/uniuri"
	"github.com/go-logr/logr"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
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
	"github.com/fission/fission/pkg/utils/leaderelection"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	// Executor defines a fission function executor.
	Executor struct {
		logger logr.Logger

		executorTypes map[fv1.ExecutorType]executortype.ExecutorType
		cms           *cms.ConfigSecretController

		fissionClient versioned.Interface

		requestChan chan *createFuncServiceRequest
		fsCreateWg  sync.Map

		// Readiness state. /readyz reports ready only when this process is the
		// leader (or leader election is disabled) AND informer caches have
		// synced, so non-leaders are kept out of the Service endpoints and a
		// just-elected leader is not advertised before its caches are warm.
		leaderElection bool
		elector        *leaderelection.Elector
		cachesSynced   atomic.Bool
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
)

// awaitLeading blocks until leadership is acquired (leadingCh is closed) or ctx
// is cancelled. Returns true if leadership was acquired, false if ctx ended
// first. When leader election is disabled the caller passes a pre-closed
// channel, so this returns true immediately.
func awaitLeading(ctx context.Context, leadingCh <-chan struct{}) bool {
	select {
	case <-leadingCh:
		return true
	case <-ctx.Done():
		return false
	}
}

// MakeExecutor returns an Executor for given ExecutorType(s). All mutating
// background work (configmap/secret informers that drive re-specialization,
// the per-type controllers and reapers, and the function-service serializer)
// is gated on leadership via leadingCh so it runs only on the elected leader.
// leadingCh is pre-closed when leader election is disabled, preserving the
// historical single-replica behaviour.
func MakeExecutor(ctx context.Context, logger logr.Logger, mgr manager.Interface, cms *cms.ConfigSecretController,
	fissionClient versioned.Interface, types map[fv1.ExecutorType]executortype.ExecutorType,
	leadingCh <-chan struct{}, informers ...k8sCache.SharedIndexInformer) (*Executor, error) {
	executor := &Executor{
		logger:        logger.WithName("executor"),
		cms:           cms,
		fissionClient: fissionClient,
		executorTypes: types,

		requestChan: make(chan *createFuncServiceRequest),
	}

	// Run all informers (leader only)
	for _, informer := range informers {
		mgr.Add(ctx, func(ctx context.Context) {
			if !awaitLeading(ctx, leadingCh) {
				return
			}
			informer.Run(ctx.Done())
		})
	}

	for _, et := range types {
		mgr.Add(ctx, func(ctx context.Context) {
			if !awaitLeading(ctx, leadingCh) {
				return
			}
			et.Run(ctx, mgr)
		})
	}
	mgr.Add(ctx, func(ctx context.Context) {
		if !awaitLeading(ctx, leadingCh) {
			return
		}
		executor.serveCreateFuncServices(ctx)
	})
	return executor, nil
}

// All non-cached function service requests go through this goroutine
// serially. It parallelizes requests for different functions, and
// ensures that for a given function, only one request causes a pod to
// get specialized. In other words, it ensures that when there's an
// ongoing request for a certain function, all other requests wait for
// that request to complete.
func (executor *Executor) serveCreateFuncServices(ctx context.Context) {
	for {
		var req *createFuncServiceRequest
		select {
		case <-ctx.Done():
			return
		case req = <-executor.requestChan:
		}
		function := req.function
		fnName := k8sCache.MetaObjectToName(function)
		fnkeyUR := crd.CacheKeyURFromObject(function)

		if req.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
			go func() {
				buffer := 10 // add some buffer time for specialization
				specializationTimeout := max(
					// set minimum specialization timeout to avoid illegal input and
					// compatibility problem when applying old spec file that doesn't
					// have specialization timeout field.
					req.function.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout, fv1.DefaultSpecializationTimeOut)

				fnSpecializationTimeoutContext, cancel := context.WithTimeoutCause(req.context,
					time.Duration(specializationTimeout+buffer)*time.Second, fmt.Errorf("function specialization timeout (%d)s exceeded", specializationTimeout+buffer))
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
		wg, found := executor.fsCreateWg.Load(fnkeyUR)
		if !found {
			// create a waitgroup for other requests for
			// the same function to wait on
			wg := &sync.WaitGroup{}
			executor.fsCreateWg.Store(fnkeyUR, wg)

			// launch a goroutine for each request, to parallelize
			// the specialization of different functions
			wg.Go(func() {
				// Control overall specialization time by setting function
				// specialization time to context. The reason not to use
				// context from router requests is because a request maybe
				// canceled for unknown reasons and let executor keeps
				// spawning pods that never finish specialization process.
				// Also, even a request failed, a specialized function pod
				// still can serve other subsequent requests.

				buffer := 10 // add some buffer time for specialization
				specializationTimeout := max(
					// set minimum specialization timeout to avoid illegal input and
					// compatibility problem when applying old spec file that doesn't
					// have specialization timeout field.
					req.function.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout, fv1.DefaultSpecializationTimeOut)

				fnSpecializationTimeoutContext, cancel := context.WithTimeoutCause(req.context,
					time.Duration(specializationTimeout+buffer)*time.Second, fmt.Errorf("function specialization timeout (%d)s exceeded", specializationTimeout+buffer))
				defer cancel()

				fsvc, err := executor.createServiceForFunction(fnSpecializationTimeoutContext, req.function)
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
					err:     err,
				}
				executor.fsCreateWg.Delete(fnkeyUR)
			})
		} else {
			// There's an existing request for this function, wait for it to finish
			go func() {
				executor.logger.V(1).Info("waiting for concurrent request for the same function",
					"function", fnName.String())
				wg, ok := wg.(*sync.WaitGroup)
				if !ok {
					err := fmt.Errorf("could not convert value to workgroup for function %s", fnName)
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
				err = fmt.Errorf("error getting service for function %s: %w", fnName, err)
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
	logger.V(1).Info("no cached function service found, creating one",
		"function_name", fn.Name,
		"function_namespace", fn.Namespace)

	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	e, ok := executor.executorTypes[t]
	if !ok {
		return nil, fmt.Errorf("unknown executor type '%s'", t)
	}

	fsvc, fsvcErr := e.GetFuncSvc(ctx, fn)
	if fsvcErr != nil {
		e := "error creating service for function"
		logger.Error(fsvcErr, e, "function_name", fn.Name,
			"function_namespace", fn.Namespace)
		fsvcErr = fmt.Errorf("[%s] %s: %w", fn.Name, e, fsvcErr)
	}

	return fsvc, fsvcErr
}

func (executor *Executor) getFunctionServiceFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	otelUtils.SpanTrackEvent(ctx, "getFunctionServiceFromCache", otelUtils.GetAttributesForFunction(fn)...)
	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	e, ok := executor.executorTypes[t]
	if !ok {
		return nil, fmt.Errorf("unknown executor type '%s'", t)
	}
	return e.GetFuncSvcFromCache(ctx, fn)
}

// StartExecutor Starts executor and the executor components such as Poolmgr,
// deploymgr and potential future executor types
func StartExecutor(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr manager.Interface, port int) error {

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("error making the fission client: %w", err)
	}
	kubernetesClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("error making the kube client: %w", err)
	}
	metricsClient, err := clientGen.GetMetricsClient()
	if err != nil {
		logger.Error(err, "error making the metrics client")
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	if err != nil {
		return fmt.Errorf("error making fetcher config: %w", err)
	}

	executorInstanceID := strings.ToLower(uniuri.NewLen(8))

	podSpecPatch, err := util.GetSpecFromConfigMap(fv1.RuntimePodSpecPath)
	if err != nil && !os.IsNotExist(err) {
		logger.Error(err, "error reading data for pod spec patch", "path", fv1.RuntimePodSpecPath)
	}

	logger.Info("Starting executor", "instanceID", executorInstanceID)

	finformerFactory := make(map[string]genInformer.SharedInformerFactory, 0)
	for _, ns := range utils.DefaultNSResolver().FissionResourceNS {
		finformerFactory[ns] = genInformer.NewSharedInformerFactoryWithOptions(fissionClient, time.Minute*30, genInformer.WithNamespace(ns))
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
		return fmt.Errorf("pool manager creation failed: %w", err)
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
		return fmt.Errorf("new deploy manager creation failed: %w", err)
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
		return fmt.Errorf("container manager creation failed: %w", err)
	}

	executorTypes := make(map[fv1.ExecutorType]executortype.ExecutorType)
	executorTypes[gpm.GetTypeName(ctx)] = gpm
	executorTypes[ndm.GetTypeName(ctx)] = ndm
	executorTypes[cnm.GetTypeName(ctx)] = cnm

	adoptExistingResources, _ := strconv.ParseBool(os.Getenv("ADOPT_EXISTING_RESOURCES"))

	// Leader election (opt-in via LEADER_ELECTION_ENABLED). Disabled by
	// default: the elector reports itself leader immediately, so every gated
	// path below runs exactly as it did historically. When enabled, only the
	// elected leader runs the mutating controllers/reapers and is advertised
	// Ready; on losing leadership we cancel runCtx so the process drains and
	// restarts (the standby then takes over). Informer factories run on every
	// replica so a standby keeps warm caches for fast failover.
	leaderElectionEnabled, _ := strconv.ParseBool(os.Getenv("LEADER_ELECTION_ENABLED"))
	runCtx, cancelRun := context.WithCancel(ctx)
	leNamespace := leaderelection.Namespace()
	if leaderElectionEnabled && leNamespace == "" {
		cancelRun()
		return fmt.Errorf("leader election enabled but pod namespace is unknown; set POD_NAMESPACE")
	}
	elector := leaderelection.New(leaderElectionEnabled, kubernetesClient, leNamespace,
		"fission-executor", leaderelection.Identity(), logger,
		leaderelection.WithOnStoppedLeading(cancelRun))
	mgr.Add(ctx, func(context.Context) {
		elector.Run(runCtx)
	})
	leadingCh := elector.Leading()

	runAdoptCleanup := func(ctx context.Context) {
		wg := &sync.WaitGroup{}
		for _, et := range executorTypes {
			wg.Go(func() {
				if adoptExistingResources {
					et.AdoptExistingResources(ctx)
				}
				et.CleanupOldExecutorObjects(ctx)
			})
		}
		// set hard timeout for resource adoption
		// TODO: use context to control the waiting time once kubernetes client supports it.
		util.WaitTimeout(wg, 30*time.Second)
	}
	if leaderElectionEnabled {
		// Can't block startup waiting for an election; defer to the leader.
		mgr.Add(runCtx, func(ctx context.Context) {
			if awaitLeading(ctx, leadingCh) {
				runAdoptCleanup(ctx)
			}
		})
	} else {
		runAdoptCleanup(ctx)
	}

	configMapInformer := utils.GetK8sInformersForNamespaces(kubernetesClient, time.Minute*30, fv1.ConfigMaps)
	secretInformer := utils.GetK8sInformersForNamespaces(kubernetesClient, time.Minute*30, fv1.Secrets)
	cms, err := cms.MakeConfigSecretController(ctx, logger, fissionClient, kubernetesClient, executorTypes, configMapInformer, secretInformer)
	if err != nil {
		cancelRun()
		return fmt.Errorf("error creating configmap and secret controller: %w", err)
	}

	fissionInformers := make([]k8sCache.SharedIndexInformer, 0)
	for _, informer := range configMapInformer {
		fissionInformers = append(fissionInformers, informer)
	}
	for _, informer := range secretInformer {
		fissionInformers = append(fissionInformers, informer)
	}
	// Informer factories run on every replica (read-only watches) so a standby
	// keeps warm caches; runCtx stops them if this leader loses its lease.
	for _, factory := range finformerFactory {
		factory.Start(runCtx.Done())
	}
	for _, informerFactory := range gpmInformerFactory {
		informerFactory.Start(runCtx.Done())
	}
	for _, informerFactory := range ndmInformerFactory {
		informerFactory.Start(runCtx.Done())
	}
	for _, informerFactory := range cnmInformerFactory {
		informerFactory.Start(runCtx.Done())
	}

	api, err := MakeExecutor(runCtx, logger, mgr, cms, fissionClient, executorTypes,
		leadingCh, fissionInformers...,
	)
	if err != nil {
		cancelRun()
		return err
	}
	api.leaderElection = leaderElectionEnabled
	api.elector = elector

	// Flip readiness on once we are the leader (or election is disabled) and
	// the function informers have synced, so /readyz only reports Ready when
	// this replica can actually serve.
	mgr.Add(runCtx, func(ctx context.Context) {
		if !awaitLeading(ctx, leadingCh) {
			return
		}
		synced := true
		for _, factory := range finformerFactory {
			for _, ok := range factory.WaitForCacheSync(ctx.Done()) {
				if !ok {
					synced = false
				}
			}
		}
		if synced {
			api.cachesSynced.Store(true)
			logger.Info("executor caches synced; ready to serve")
		}
	})

	utils.CreateMissingPermissionForSA(ctx, kubernetesClient, logger)

	mgr.Add(runCtx, func(ctx context.Context) {
		metrics.ServeMetrics(ctx, "executor", logger, mgr)
	})

	mgr.Add(runCtx, func(ctx context.Context) {
		api.Serve(ctx, mgr, port)
	})

	return nil
}
