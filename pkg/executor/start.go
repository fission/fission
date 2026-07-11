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
	"time"

	"github.com/dchest/uniuri"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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
	"github.com/fission/fission/pkg/executor/funcreconciler"
	"github.com/fission/fission/pkg/executor/reaper/idle"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/tenant"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpserver"
	fissionmetrics "github.com/fission/fission/pkg/utils/metrics"
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
	// The pool manager's readyPod reconciler and pod reads watch Pods through this
	// cache (replacing gpmInformerFactory). Scope the Pod watch to pool-manager
	// pods so the cache doesn't mirror every function pod in the function
	// namespace — the same executor-label filter the old informer used.
	//
	// Deployments + Services are watched for the newdeploy/container managers'
	// IsValid reads (replacing their standalone SharedInformerFactory). Scope them
	// to executor-managed objects so the cache doesn't mirror every
	// Deployment/Service in the namespace — the same label bounding the old
	// factories applied (issue #2775).
	byObject := map[client.Object]crcache.ByObject{
		&corev1.Pod{}: {
			Label: labels.SelectorFromSet(labels.Set{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr)}),
		},
		&appsv1.Deployment{}: {Label: executorManagedSelector},
		&corev1.Service{}:    {Label: executorManagedSelector},
	}

	if utils.ClusterTenancyEnabled() {
		// Cluster (trusted-cluster) mode: Tier A AND Tier B go cluster-wide. The
		// executor reads Secrets/ConfigMaps/ReplicaSets across all namespaces — the
		// operational simplification this opt-in mode trades isolation for (PRD
		// §4.5; the executor holds the matching cluster-wide read via
		// cluster-mode-bindings.yaml). The label-bounded Pod/Deployment/Service
		// watches above are kept (memory bounds, valid in any mode); Secret/
		// ConfigMap/ReplicaSet get NO per-namespace override, so they default to the
		// cluster-wide cache. Function pods still hold only narrow per-namespace
		// fetcher RBAC — this widening is the control plane's, not the workload's.
		return crcache.Options{ByObject: byObject}
	}

	if utils.DynamicNamespacesEnabled() {
		// Tier A (Function/Environment) goes cluster-wide so a namespace onboarded
		// at runtime is visible without a restart; the func/env reconcilers filter
		// to the live tenant set via controller.MembershipPredicate. The labeled
		// workload watches above are already label-bounded, so cluster-wide +
		// label-filtered is safe (pool pods / managed Deployments/Services are not
		// tenant secrets — PRD §4.1).
		//
		// Tier B (Secret/ConfigMap) MUST stay namespace-scoped: a cluster-wide
		// Secret cache would mirror every Secret in the cluster into the executor's
		// memory, the one read the design forbids. Override the per-type namespaces
		// to the env-seeded set, leaving the cluster-wide default to the Tier-A CRDs
		// only. (Operators needing Tier-B reads in arbitrary runtime-onboarded
		// namespaces use cluster mode, which goes cluster-wide above.)
		byObject[&corev1.Secret{}] = crcache.ByObject{Namespaces: nsConfig}
		byObject[&corev1.ConfigMap{}] = crcache.ByObject{Namespaces: nsConfig}
		// ReplicaSets (poolmgr's specialized-pod cleanup watch) are not label-bounded
		// here, so a cluster-wide cache would mirror every ReplicaSet in the cluster.
		// Keep them namespace-scoped too — and out of the cluster-wide RBAC.
		byObject[&appsv1.ReplicaSet{}] = crcache.ByObject{Namespaces: nsConfig}
		return crcache.Options{ByObject: byObject}
	}

	return crcache.Options{
		DefaultNamespaces: nsConfig,
		ByObject:          byObject,
	}
}

// executorManagedSelector matches the Deployments and Services created by the
// newdeploy and container executor types (which label them with EXECUTOR_TYPE).
// It bounds the Manager cache's Deployment/Service watch to executor-managed
// objects, preserving the label scoping the standalone informer factories had so
// the cache doesn't mirror every Deployment/Service in the function namespace
// (issue #2775).
var executorManagedSelector = func() labels.Selector {
	req, err := labels.NewRequirement(fv1.EXECUTOR_TYPE, selection.In,
		[]string{string(fv1.ExecutorTypeNewdeploy), string(fv1.ExecutorTypeContainer)})
	if err != nil {
		// Inputs are compile-time constants, so this cannot fail in practice.
		panic(fmt.Sprintf("building executor-managed label selector: %v", err))
	}
	return labels.NewSelector().Add(*req)
}()

// executorControllers runs the executor's mutating controllers on the elected
// leader only (NeedLeaderElection). When leader election is disabled the Manager
// runs it unconditionally, preserving single-replica behaviour. Non-leaders
// therefore never start it, so /readyz (served by the API server on every
// replica) reports not-ready and the Service excludes them.
type executorControllers struct {
	logger         logr.Logger
	api            *Executor
	executorTypes  map[fv1.ExecutorType]executortype.ExecutorType
	waitForSync    func(ctx context.Context) bool
	adoptResources bool
}

func (c *executorControllers) NeedLeaderElection() bool { return true }

func (c *executorControllers) Start(ctx context.Context) error {
	gm := &errgroup.Group{}

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

	runAdoptCleanup(ctx, c.executorTypes, c.adoptResources)

	c.api.isLeader.Store(true)
	go c.markSyncedWhenCachesWarm(ctx)

	<-ctx.Done()
	c.api.isLeader.Store(false)
	_ = gm.Wait()
	return nil
}

// markSyncedWhenCachesWarm flips the readiness bit once the Manager cache has
// synced, so /readyz starts reporting ready on the leader.
func (c *executorControllers) markSyncedWhenCachesWarm(ctx context.Context) {
	if c.waitForSync(ctx) {
		c.api.cachesSynced.Store(true)
		c.logger.Info("executor caches synced; ready to serve")
	}
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

	// newdeploy/container read Deployments/Services from the Manager cache (their
	// IsValid path); the cache replaces the per-type SharedInformerFactory they
	// used to run. The cache-backed client is wired in via RegisterReconcilers.
	ndm, err := newdeploy.MakeNewDeploy(ctx,
		logger,
		fissionClient, kubernetesClient,
		fetcherConfig, executorInstanceID,
		podSpecPatch)
	if err != nil {
		return fmt.Errorf("new deploy manager creation failed: %w", err)
	}

	cnm, err := container.MakeContainer(
		ctx, logger,
		fissionClient, kubernetesClient,
		executorInstanceID)
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

	// Bound on concurrently running specializations (RFC-0002 phase 0b);
	// 0 / unset keeps the historical unbounded behavior.
	specializationConcurrency, _ := strconv.Atoi(os.Getenv("EXECUTOR_SPECIALIZATION_CONCURRENCY"))

	api := MakeExecutor(logger, fissionClient, executorTypes, specializationConcurrency)
	api.leaderElection = leaderElectionEnabled

	// Fission's collectors register into controller-runtime's global registry;
	// the Manager's metrics server then serves them on METRICS_ADDR (:8080).
	var alreadyRegistered prometheus.AlreadyRegisteredError
	if err := ctrlmetrics.Registry.Register(fissionmetrics.Registry); err != nil && !errors.As(err, &alreadyRegistered) {
		logger.Error(err, "failed to register fission metrics collectors")
	}

	metricsBind := httpserver.BindAddrFromEnv("METRICS_ADDR", svcinfo.PortMetrics)
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
	//
	// FINALIZER_ENABLED toggles the cleanup finalizer for reliable cross-namespace
	// teardown. The Helm chart sets it from the chart-wide finalizerEnabled value
	// (default true); when off, any existing finalizer is drained. A bare binary
	// with the env unset defaults to off (conservative). See
	// funcreconciler.functionFinalizer.
	finalizerEnabled, _ := strconv.ParseBool(os.Getenv("FINALIZER_ENABLED"))
	if err := funcreconciler.RegisterReconciler(crMgr, logger, executorTypes, finalizerEnabled); err != nil {
		return err
	}

	// One Environment reconciler, shared across executor types: it dispatches each
	// Environment event to every type implementing EnvReconciler (poolmgr pool sync
	// + newdeploy image propagation), replacing the per-type Environment reconcilers
	// with a single workqueue and last-seen cache.
	if err := envreconciler.RegisterReconciler(crMgr, logger, executorTypes); err != nil {
		return err
	}

	// Cross-process propagation: under dynamic tenancy keep the executor's resolver
	// in step with the FissionTenant set so a namespace onboarded at runtime is
	// admitted by the func/env membership predicates (and gets pooled/specialized)
	// without a restart. The cache is cluster-wide for Tier-A types in this mode
	// (executorCacheOptions); the tenant controller still owns provisioning.
	// AddResolverSync is a no-op when dynamic tenancy is off.
	if err := tenant.AddResolverSync(crMgr); err != nil {
		return fmt.Errorf("unable to add tenant resolver-sync: %w", err)
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
