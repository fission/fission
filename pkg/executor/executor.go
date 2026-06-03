// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dchest/uniuri"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8sCache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/cms"
	"github.com/fission/fission/pkg/executor/envreconciler"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/executortype/container"
	"github.com/fission/fission/pkg/executor/executortype/newdeploy"
	"github.com/fission/fission/pkg/executor/executortype/poolmgr"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/funcreconciler"
	"github.com/fission/fission/pkg/executor/reaper/idle"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils"
	fissionmetrics "github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// executorScheme is the controller-runtime Manager's scheme. Unlike the
// generated fission scheme.Scheme (Fission CRD types only), it also registers
// the Kubernetes built-in types the executor's reconcilers watch — starting with
// ConfigMap + Secret (cms), and Pods/Deployments/ReplicaSets as later executor
// pieces migrate.
var executorScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(executorScheme))
	utilruntime.Must(scheme.AddToScheme(executorScheme))
}

// executorCacheOptions scopes the Manager cache to exactly the namespaces the
// executor's k8s-native informers covered before the migration: FissionResourceNS
// plus the builder, function, and default namespaces (matching
// GetK8sInformersForNamespaces). ConfigMaps/Secrets mounted by functions — and,
// for later executor pieces, the function pods/deployments — live in the
// function/builder namespaces, so scoping to FissionResourceNS alone
// (crmanager.FissionCacheOptions) would miss them in installs that set a separate
// FISSION_FUNCTION_NAMESPACE / FISSION_BUILDER_NAMESPACE. Executor RBAC is
// per-namespace Roles in these same namespaces, so a cluster-wide cache would be
// forbidden and crashloop on cache-sync timeout.
func executorCacheOptions() crcache.Options {
	resolver := utils.DefaultNSResolver()
	nsConfig := map[string]crcache.Config{}
	for _, ns := range resolver.FissionNSWithOptions(utils.WithBuilderNs(), utils.WithFunctionNs(), utils.WithDefaultNs()) {
		nsConfig[ns] = crcache.Config{}
	}
	return crcache.Options{
		DefaultNamespaces: nsConfig,
		// The pool manager's readyPod reconciler and pod reads watch Pods through
		// this cache (replacing gpmInformerFactory). Scope the Pod watch to
		// pool-manager pods so the cache doesn't mirror every function pod in the
		// function namespace — the same executor-label filter the old informer used.
		ByObject: map[client.Object]crcache.ByObject{
			&corev1.Pod{}: {
				Label: labels.SelectorFromSet(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr)}),
			},
		},
	}
}

type (
	// Executor defines a fission function executor.
	Executor struct {
		logger logr.Logger

		executorTypes map[fv1.ExecutorType]executortype.ExecutorType

		fissionClient versioned.Interface

		requestChan chan *createFuncServiceRequest
		fsCreateWg  sync.Map

		// Readiness state. /readyz reports ready only when this process is the
		// leader (or leader election is disabled) AND informer caches have
		// synced, so non-leaders are kept out of the Service endpoints and a
		// just-elected leader is not advertised before its caches are warm.
		// isLeader is set by the leader-only controllers runnable under the
		// controller-runtime Manager.
		leaderElection bool
		isLeader       atomic.Bool
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

// MakeExecutor returns an Executor for the given ExecutorType(s). It only builds
// the object; the mutating controllers are started by executorControllers (a
// leader-only runnable) under the controller-runtime Manager.
func MakeExecutor(logger logr.Logger,
	fissionClient versioned.Interface, types map[fv1.ExecutorType]executortype.ExecutorType) *Executor {
	return &Executor{
		logger:        logger.WithName("executor"),
		fissionClient: fissionClient,
		executorTypes: types,

		requestChan: make(chan *createFuncServiceRequest),
	}
}

// executorControllers runs the executor's mutating controllers on the elected
// leader only (NeedLeaderElection). When leader election is disabled the Manager
// runs it unconditionally, preserving single-replica behaviour. Non-leaders
// therefore never start it, so /readyz (served by the API server on every
// replica) reports not-ready and the Service excludes them.
type executorControllers struct {
	logger         logr.Logger
	api            *Executor
	executorTypes  map[fv1.ExecutorType]executortype.ExecutorType
	startFactories func(stopCh <-chan struct{})
	waitForSync    func(ctx context.Context) bool
	adoptResources bool
}

func (c *executorControllers) NeedLeaderElection() bool { return true }

func (c *executorControllers) Start(ctx context.Context) error {
	gm := &errgroup.Group{}

	// Read-only executortype informer factories (listers used by et.Run).
	c.startFactories(ctx.Done())

	for _, et := range c.executorTypes {
		gm.Go(func() error { et.Run(ctx, gm); return nil })
	}

	// One shared idle reaper drives every executor type's idle-reaping strategy
	// (replacing the three per-type reaper goroutines). It runs on the leader
	// only, inheriting this leader-elected runnable's context.
	strategies := make([]idle.Strategy, 0, len(c.executorTypes))
	for _, et := range c.executorTypes {
		strategies = append(strategies, et.IdleStrategy())
	}
	idleReaper := idle.NewReaper(c.logger, strategies...)
	gm.Go(func() error { idleReaper.Start(ctx); return nil })

	gm.Go(func() error { c.api.serveCreateFuncServices(ctx); return nil })

	runAdoptCleanup(ctx, c.executorTypes, c.adoptResources)

	c.api.isLeader.Store(true)
	go func() {
		if c.waitForSync(ctx) {
			c.api.cachesSynced.Store(true)
			c.logger.Info("executor caches synced; ready to serve")
		}
	}()

	<-ctx.Done()
	c.api.isLeader.Store(false)
	_ = gm.Wait()
	return nil
}

// executorAPIServer serves the executor HTTP API (getServiceForFunction plus the
// /healthz and /readyz probes) on every replica, so non-leaders report
// not-ready and are kept out of the Service endpoints.
type executorAPIServer struct {
	api  *Executor
	port int
}

func (a *executorAPIServer) NeedLeaderElection() bool { return false }

func (a *executorAPIServer) Start(ctx context.Context) error {
	gm := &errgroup.Group{}
	a.api.Serve(ctx, gm, a.port)
	_ = gm.Wait()
	return nil
}

// adoptCleanupMaxWait caps how long startup readiness waits for the adopt +
// cleanup pass before proceeding. It's a safety bound only: the pass itself is
// context-bound and continues in the background if it outlasts this, and
// CleanupOldExecutorObjects runs after AdoptExistingResources within each
// goroutine regardless, so it never reaps objects adopt hasn't re-stamped.
const adoptCleanupMaxWait = 30 * time.Second

// runAdoptCleanup adopts pre-existing resources (optional) and cleans up stale
// executor objects. Runs on the leader.
func runAdoptCleanup(ctx context.Context, executorTypes map[fv1.ExecutorType]executortype.ExecutorType, adopt bool) {
	wg := &sync.WaitGroup{}
	for _, et := range executorTypes {
		wg.Go(func() {
			if adopt {
				et.AdoptExistingResources(ctx)
			}
			et.CleanupOldExecutorObjects(ctx)
		})
	}
	// Wait for the pass, but honour ctx (executor shutdown / leader-lease loss)
	// rather than blocking on a fixed timer that ignores it — the adopt/cleanup
	// calls are themselves ctx-bound and return promptly on cancellation. The
	// cap bounds startup readiness if a call wedges.
	done := make(chan struct{})
	go func() { defer close(done); wg.Wait() }()
	timer := time.NewTimer(adoptCleanupMaxWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
	case <-timer.C:
	}
}

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
func StartExecutor(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr *errgroup.Group, port int) error {

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
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("error getting rest config: %w", err)
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

	// Function and Environment reads go through the executor Manager cache now, so
	// no dedicated fission informer factory is needed.
	gpm, err := poolmgr.MakeGenericPoolManager(ctx,
		logger,
		fissionClient, kubernetesClient, metricsClient,
		fetcherConfig, executorInstanceID,
		podSpecPatch)
	if err != nil {
		return fmt.Errorf("pool manager creation failed: %w", err)
	}

	executorLabel, err := utils.GetInformerLabelByExecutor(fv1.ExecutorTypeNewdeploy)
	if err != nil {
		return err
	}
	ndmInformerFactory := utils.GetInformerFactoryByExecutor(kubernetesClient, executorLabel, time.Minute*30)
	ndm, err := newdeploy.MakeNewDeploy(ctx,
		logger,
		fissionClient, kubernetesClient,
		fetcherConfig, executorInstanceID,
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
		executorInstanceID,
		cnmInformerFactory)
	if err != nil {
		return fmt.Errorf("container manager creation failed: %w", err)
	}

	executorTypes := make(map[fv1.ExecutorType]executortype.ExecutorType)
	executorTypes[gpm.GetTypeName(ctx)] = gpm
	executorTypes[ndm.GetTypeName(ctx)] = ndm
	executorTypes[cnm.GetTypeName(ctx)] = cnm

	adoptExistingResources, _ := strconv.ParseBool(os.Getenv("ADOPT_EXISTING_RESOURCES"))

	// Leader election is owned by the controller-runtime Manager (native),
	// opt-in via LEADER_ELECTION_ENABLED. When disabled the Manager runs every
	// runnable unconditionally, so single-replica behaviour is unchanged. On
	// lease loss the Manager stops and the API server's /healthz (port 8888)
	// goes down, so the kubelet restarts the pod and it rejoins as a standby.
	leaderElectionEnabled, _ := strconv.ParseBool(os.Getenv("LEADER_ELECTION_ENABLED"))

	api := MakeExecutor(logger, fissionClient, executorTypes)
	api.leaderElection = leaderElectionEnabled

	// Fission's collectors register into controller-runtime's global registry;
	// the Manager's metrics server then serves them on METRICS_ADDR (:8080).
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
		Scheme: executorScheme,
		// Scope the cache to the executor's watched namespaces (builder +
		// function + default + resource), matching the informers it replaces.
		// See executorCacheOptions.
		Cache:                         executorCacheOptions(),
		Metrics:                       metricsserver.Options{BindAddress: metricsBind},
		HealthProbeBindAddress:        "0", // /healthz + /readyz stay on the API mux (port)
		LeaderElection:                leaderElectionEnabled,
		LeaderElectionID:              "fission-executor",
		LeaderElectionReleaseOnCancel: true,
		Logger:                        logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up executor manager: %w", err)
	}

	// ConfigMap/Secret changes recycle the pods of functions that mount them.
	// These reconcilers replace the cms informer event handlers and, like the
	// other mutating controllers, run on the elected leader only.
	if err := cms.RegisterReconcilers(crMgr, logger, fissionClient, executorTypes); err != nil {
		return err
	}

	// Each executor type registers its Function/Environment reconcilers on the
	// Manager (replacing its informer event handlers): container (Function),
	// newdeploy (Function + Environment), poolmgr (Function + Environment). The
	// remaining poolmgr watches (ReplicaSet → specialized-pod cleanup) stay on
	// poolpodcontroller's informers.
	for _, et := range executorTypes {
		if err := et.RegisterReconcilers(crMgr); err != nil {
			return fmt.Errorf("error registering reconcilers for executor type %s: %w", et.GetTypeName(ctx), err)
		}
	}

	// One Function reconciler, shared across executor types: it resolves each
	// Function's executor type and dispatches create/update/delete to the owning
	// type (poolmgr/newdeploy/container) via FuncReconciler, handling executor-type
	// transitions in one place. Replaces the three per-type Function reconcilers
	// with a single workqueue, predicate, and last-reconciled cache.
	if err := funcreconciler.RegisterReconciler(crMgr, logger, executorTypes); err != nil {
		return err
	}

	// One Environment reconciler, shared across executor types: it dispatches each
	// Environment event to every type implementing EnvReconciler (poolmgr pool sync
	// + newdeploy image propagation), replacing the per-type Environment reconcilers
	// with a single workqueue and last-seen cache.
	if err := envreconciler.RegisterReconciler(crMgr, logger, executorTypes); err != nil {
		return err
	}

	startFactories := func(stopCh <-chan struct{}) {
		for _, factory := range ndmInformerFactory {
			factory.Start(stopCh)
		}
		for _, factory := range cnmInformerFactory {
			factory.Start(stopCh)
		}
	}
	waitForSync := func(syncCtx context.Context) bool {
		// The executor's Function/Environment/ConfigMap/Secret/Pod/ReplicaSet reads
		// all go through the Manager cache now; it's ready once that has synced.
		// controller-runtime syncs the cache before starting this (non-cache)
		// runnable, so this returns ~immediately. syncCtx is the runnable's context,
		// so the wait is cancelled on shutdown or leadership loss.
		return crMgr.GetCache().WaitForCacheSync(syncCtx)
	}

	utils.CreateMissingPermissionForSA(ctx, kubernetesClient, logger)

	controllers := &executorControllers{
		logger:         logger,
		api:            api,
		executorTypes:  executorTypes,
		startFactories: startFactories,
		waitForSync:    waitForSync,
		adoptResources: adoptExistingResources,
	}
	if err := crMgr.Add(controllers); err != nil {
		return fmt.Errorf("unable to add executor controllers: %w", err)
	}
	if err := crMgr.Add(&executorAPIServer{api: api, port: port}); err != nil {
		return fmt.Errorf("unable to add executor api server: %w", err)
	}

	logger.Info("starting executor manager", "instanceID", executorInstanceID, "leaderElection", leaderElectionEnabled)
	return crMgr.Start(ctx)
}
