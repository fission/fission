// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/cache"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/metrics"
	"github.com/fission/fission/pkg/executor/reaper"
	"github.com/fission/fission/pkg/executor/reaper/idle"
	executorUtils "github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

var (
	_ executortype.ExecutorType = &GenericPoolManager{}
)

type requestType int

const (
	GET_POOL requestType = iota
	CLEANUP_POOL
	GET_ENV_POOLS
	REAP_IDLE_POOLS
)

type (
	GenericPoolManager struct {
		logger logr.Logger

		// pools is keyed by poolKey(env UID, image hash): the env UID alone
		// for plain fetcher-based pools, or env UID + "/" + image hash for
		// per-image image-volume pools (RFC-0001 Path B).
		pools            map[string]*GenericPool
		kubernetesClient kubernetes.Interface
		metricsClient    metricsclient.Interface
		nsResolver       *utils.NamespaceResolver

		fissionClient  versioned.Interface
		functionEnv    *cache.Cache[crd.CacheKeyUR, *fv1.Environment]
		fsCache        *fscache.FunctionServiceCache
		instanceID     string
		requestChannel chan *request

		// crClient is the executor Manager's cache-backed client, used by
		// getFunctionEnv to resolve a function's Environment and by the pool /
		// reconcilers to read pods (replacing poolpodcontroller's and the per-pool
		// informer listers). Set in RegisterReconcilers.
		crClient client.Client

		// readyPodQueues maps pool key (poolKey(env UID, image hash)) -> a
		// pool's readyPodQueue, published by the actor on pool create and
		// removed on destroy. The Pod reconciler reads it lock-free
		// (sync.Map) to feed warm pods into the right pool's queue.
		readyPodQueues sync.Map

		// imageVolumeOK is the once-evaluated RFC-0001 Path B gate:
		// ENABLE_OCI_IMAGE_VOLUME opted in AND the cluster supports
		// KEP-4639 image volumes (>= 1.33).
		imageVolumeOK bool

		enableIstio   bool
		fetcherConfig *fetcherConfig.Config

		defaultIdlePodReapTime time.Duration
		// ociPoolIdleReapTime is the idle window after which an empty
		// per-image pool deployment is reaped (RFC-0012; OCI_POOL_IDLE_REAP_TIME).
		ociPoolIdleReapTime time.Duration

		poolPodC *PoolPodController

		podSpecPatch               *apiv1.PodSpec
		objectReaperIntervalSecond time.Duration

		// podReadyTimeout bounds how long choosePod waits for a warm pod; parsed
		// once from POD_READY_TIMEOUT and handed to every pool.
		podReadyTimeout time.Duration

		// maxPendingSpecializations bounds specializations in flight per
		// function on the ensureCapacity path (0 disables); parsed once from
		// EXECUTOR_MAX_PENDING_SPECIALIZATIONS_PER_FUNCTION. See ReserveCapacity.
		maxPendingSpecializations int

		// functionServicesEnabled is the RFC-0002 gate (ENABLE_FUNCTION_SERVICES):
		// when on, every invoked poolmgr function gets a headless selector
		// Service so the EndpointSlice controller publishes its specialized
		// pods to the router's slice-fed index. Disabled in Istio mode, whose
		// functions are addressed via Istio services instead.
		functionServicesEnabled bool

		// fnSvcEnsured debounces ensureFunctionService per function UID (see
		// maybeEnsureFunctionService): map[types.UID]time.Time of the last
		// successful-or-in-flight ensure. Entries are dropped on function
		// delete and on ensure failure (so the next request retries).
		fnSvcEnsured sync.Map
	}
	request struct {
		requestType
		ctx context.Context
		env *fv1.Environment
		// oci selects a per-image image-volume pool (RFC-0001 Path B),
		// including its pod-spec variant (RFC-0012 B-fetcher);
		// nil selects the env's plain fetcher-based pool.
		oci             *ociPoolSpec
		responseChannel chan *response
	}
	response struct {
		error
		pool    *GenericPool
		created bool
		// pools is the GET_ENV_POOLS payload: every live pool of an env
		// (plain + per-image).
		pools []*GenericPool
	}
)

func MakeGenericPoolManager(ctx context.Context,
	logger logr.Logger,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	metricsClient metricsclient.Interface,
	fetcherConfig *fetcherConfig.Config,
	instanceID string,
	podSpecPatch *apiv1.PodSpec,
) (executortype.ExecutorType, error) {

	gpmLogger := logger.WithName("generic_pool_manager")

	enableIstio := false
	if len(os.Getenv("ENABLE_ISTIO")) > 0 {
		istio, err := strconv.ParseBool(os.Getenv("ENABLE_ISTIO"))
		if err != nil {
			gpmLogger.Error(err, "failed to parse 'ENABLE_ISTIO', set to false")
		}
		enableIstio = istio
	}

	// RFC-0001 Path B gate, evaluated once (shared helper, so poolmgr and
	// newdeploy cannot drift).
	imageVolumeOK := executorUtils.ImageVolumeGate(gpmLogger, kubernetesClient.Discovery())

	poolPodC := NewPoolPodController(gpmLogger, kubernetesClient)
	gpm := &GenericPoolManager{
		logger:                     gpmLogger,
		pools:                      make(map[string]*GenericPool),
		imageVolumeOK:              imageVolumeOK,
		kubernetesClient:           kubernetesClient,
		nsResolver:                 utils.DefaultNSResolver(),
		metricsClient:              metricsClient,
		fissionClient:              fissionClient,
		functionEnv:                cache.MakeCache[crd.CacheKeyUR, *fv1.Environment](10*time.Second, 0),
		fsCache:                    fscache.MakeFunctionServiceCache(gpmLogger),
		instanceID:                 instanceID,
		requestChannel:             make(chan *request),
		defaultIdlePodReapTime:     2 * time.Minute,
		ociPoolIdleReapTime:        ociPoolIdleReapTimeFromEnv(logger),
		fetcherConfig:              fetcherConfig,
		enableIstio:                enableIstio,
		poolPodC:                   poolPodC,
		podSpecPatch:               podSpecPatch,
		objectReaperIntervalSecond: time.Duration(executorUtils.GetObjectReaperInterval(logger, fv1.ExecutorTypePoolmgr, 5)) * time.Second,
		podReadyTimeout:            podReadyTimeoutFromEnv(gpmLogger),
		maxPendingSpecializations:  maxPendingSpecializationsFromEnv(gpmLogger),
		functionServicesEnabled:    functionServicesEnabled() && !enableIstio,
	}

	gpm.logger.V(1).Info("inside MakeGenericPoolManager")

	return gpm, nil
}

func (gpm *GenericPoolManager) Run(ctx context.Context, mgr *errgroup.Group) {
	// Pod reads go through the executor Manager cache (synced before this Runnable
	// starts), so there is no informer cache to wait for here.
	go gpm.service()
	gpm.poolPodC.InjectGpm(gpm)

	mgr.Go(func() error {
		err := gpm.WebsocketStartEventChecker(ctx, gpm.kubernetesClient)
		if err != nil {
			gpm.logger.Error(err, "error in checking websocket start event from pod: ")
		}
		return nil
	})
	mgr.Go(func() error {
		err := gpm.NoActiveConnectionEventChecker(ctx, gpm.kubernetesClient) //nolint:errcheck
		if err != nil {
			gpm.logger.Error(err, "error in checking inactive event from pod: ")
		}
		return nil
	})
	mgr.Go(func() error {
		gpm.poolPodC.Run(ctx, ctx.Done(), mgr)
		return nil
	})
	// Per-image idle pool reaper (RFC-0012): bounds warm-pool economics once
	// every built package is its own image. Generic pools are never touched.
	mgr.Go(func() error {
		gpm.reapIdlePoolsLoop(ctx)
		return nil
	})
}

func (gpm *GenericPoolManager) GetTypeName(ctx context.Context) fv1.ExecutorType {
	return fv1.ExecutorTypePoolmgr
}

func (gpm *GenericPoolManager) GetFuncSvc(ctx context.Context, fn *fv1.Function) (fnSvc *fscache.FuncSvc, fErr error) {
	defer func() {
		if fErr != nil {
			metrics.RecordColdStartError(ctx, fn.Name, fn.Namespace)
			return
		}

		metrics.RecordColdStart(ctx, fn.Name, fn.Namespace)
	}()

	otelUtils.SpanTrackEvent(ctx, "GetFuncSvc", otelUtils.GetAttributesForFunction(fn)...)
	logger := otelUtils.LoggerWithTraceID(ctx, gpm.logger)

	// from Func -> get Env
	logger.V(1).Info("getting environment for function", "function", fn.Name)
	env, err := gpm.getFunctionEnv(ctx, fn)
	if err != nil {
		fErr = err
		return nil, fErr
	}

	// RFC-0001 Path B: an eligible OCI-packaged function gets a per-image
	// pool whose pods mount the code as an image volume — fetcherless
	// (B-direct), or with the fetcher retained for Secrets/ConfigMaps
	// materialization (B-fetcher, RFC-0012). nil means the plain pool
	// serves it (Path A or non-OCI). A failed eligibility read fails the
	// cold start (the router retries) rather than silently pinning the
	// function to the wrong pool type in fsCache.
	var oci *ociPoolSpec
	if gpm.imageVolumeOK {
		oci, err = gpm.getFunctionOCIPool(ctx, fn, env)
		if err != nil {
			fErr = fmt.Errorf("error reading package for OCI eligibility of function %s: %w", fn.Name, err)
			return nil, fErr
		}
	}

	pool, created, err := gpm.getPool(ctx, env, oci)
	if err != nil {
		fErr = err
		return nil, fErr
	}

	if created {
		logger.Info("created pool for the environment", "env", env.Name, "namespace", gpm.nsResolver.ResolveNamespace(gpm.nsResolver.FunctionNamespace))
	}

	// from GenericPool -> get one function container
	// (this also adds to the cache)
	logger.V(1).Info("getting function service from pool", "function", fn.Name)
	fnSvc, fErr = pool.getFuncSvc(ctx, fn)
	if fErr == nil {
		// Ensure the function's headless Service (RFC-0002) strictly off the
		// cold-start path: the pod address has already been produced; the
		// Service only feeds the router's warm-path EndpointSlice index.
		gpm.maybeEnsureFunctionService(fn)
	}
	return fnSvc, fErr
}

func (gpm *GenericPoolManager) GetFuncSvcFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	otelUtils.SpanTrackEvent(ctx, "GetFuncSvcFromCache", otelUtils.GetAttributesForFunction(fn)...)
	fnSvc, err := gpm.fsCache.GetFuncSvc(ctx, &fn.ObjectMeta, fn.GetRequestPerPod(), fn.GetConcurrency())
	if ferror.IsTooManyRequests(err) {
		// The legacy data plane's concurrency-cap rejection: count it on the
		// same series as the ensureCapacity-path rejections so saturation is
		// visible regardless of which plane served the traffic.
		metrics.RecordSpecializationRejected(ctx, fn.Name, fn.Namespace)
	}
	if err == nil {
		// Self-healing for the function Service: the one-shot ensure on the
		// cold start can be lost (executor rolled mid-ensure), and a missing
		// Service means no slices — which routes every request through this
		// very RPC path, making it the natural repair point. Debounced, so the
		// steady state adds nothing.
		gpm.maybeEnsureFunctionService(fn)
	}
	return fnSvc, err
}

func (gpm *GenericPoolManager) DeleteFuncSvcFromCache(ctx context.Context, fsvc *fscache.FuncSvc) {
	otelUtils.SpanTrackEvent(ctx, "DeleteFuncSvcFromCache", fscache.GetAttributesForFuncSvc(fsvc)...)
	gpm.fsCache.DeleteFunctionSvc(ctx, fsvc)
}

// defaultMaxPendingSpecializations bounds specializations in flight per
// function when EXECUTOR_MAX_PENDING_SPECIALIZATIONS_PER_FUNCTION is unset.
// Sized well above a legitimate cold burst (the benchmark fires 10) and well
// below the 88-pod saturation storm this bound exists to stop; 0 disables.
const defaultMaxPendingSpecializations = 20

// maxPendingSpecializationsFromEnv parses
// EXECUTOR_MAX_PENDING_SPECIALIZATIONS_PER_FUNCTION, defaulting on a missing
// or unparsable value. Called once by MakeGenericPoolManager.
func maxPendingSpecializationsFromEnv(logger logr.Logger) int {
	v := os.Getenv("EXECUTOR_MAX_PENDING_SPECIALIZATIONS_PER_FUNCTION")
	if v == "" {
		return defaultMaxPendingSpecializations
	}
	n, err := strconv.Atoi(v)
	if err == nil && n < 0 {
		err = fmt.Errorf("value must be >= 0, got %d", n)
	}
	if err != nil {
		logger.Error(err, "invalid EXECUTOR_MAX_PENDING_SPECIALIZATIONS_PER_FUNCTION - using default",
			"value", v, "default", defaultMaxPendingSpecializations)
		return defaultMaxPendingSpecializations
	}
	return n
}

// ReserveCapacity implements the executor's capacityReserver facet (RFC-0002
// ensureCapacity): an atomic check-and-reserve against the function's
// concurrency cap AND the per-function in-flight specialization bound inside
// the PoolCache — still the capacity authority. The reservation is released
// by the specialization's setValue on success or MarkSpecializationFailure on
// failure. Rejections surface to the router (and client) as 429s.
func (gpm *GenericPoolManager) ReserveCapacity(ctx context.Context, fnMeta *metav1.ObjectMeta, concurrency int) error {
	err := gpm.fsCache.ReserveCapacity(crd.CacheKeyUGFromMeta(fnMeta), concurrency, gpm.maxPendingSpecializations)
	if err != nil {
		metrics.RecordSpecializationRejected(ctx, fnMeta.Name, fnMeta.Namespace)
	}
	return err
}

func (gpm *GenericPoolManager) UnTapService(ctx context.Context, fnMeta *metav1.ObjectMeta, svcHost string) {
	key := crd.CacheKeyUGFromMeta(fnMeta)
	otelUtils.SpanTrackEvent(ctx, "UnTapService",
		attribute.KeyValue{Key: "key", Value: attribute.StringValue(key.String())},
		attribute.KeyValue{Key: "svcHost", Value: attribute.StringValue(svcHost)})
	gpm.fsCache.MarkAvailable(key, svcHost)
}

func (gpm *GenericPoolManager) TapService(ctx context.Context, svcHost string) error {
	otelUtils.SpanTrackEvent(ctx, "TapService",
		attribute.KeyValue{Key: "svcHost", Value: attribute.StringValue(svcHost)})
	err := gpm.fsCache.TouchByAddress(svcHost)
	if err != nil {
		return err
	}
	return nil
}

func (gpm *GenericPoolManager) MarkSpecializationFailure(ctx context.Context, fnMeta *metav1.ObjectMeta) {
	key := crd.CacheKeyUGFromMeta(fnMeta)
	otelUtils.SpanTrackEvent(ctx, "MarkSpecializationFailure",
		attribute.KeyValue{Key: "key", Value: attribute.StringValue(key.String())})
	// Mark the active cold-start span errored so a failed specialization shows
	// as the failing phase in the trace (RFC-0015), complementing the
	// coldstart/specialize child span set in specializePod.
	span := trace.SpanFromContext(ctx)
	span.SetStatus(codes.Error, ferror.ReasonSpecializationFailed)
	span.SetAttributes(attribute.String("coldstart.failure_reason", ferror.ReasonSpecializationFailed))
	logger := otelUtils.LoggerWithTraceID(ctx, gpm.logger)
	logger.Info("marking specialization failure", "key", key)
	gpm.fsCache.MarkSpecializationFailure(key)
}

// IsValid checks if pod is not deleted and that it has the address passed as the argument. Also checks that all the
// containers in it are reporting a ready status for the healthCheck.
func (gpm *GenericPoolManager) IsValid(ctx context.Context, fsvc *fscache.FuncSvc) bool {
	otelUtils.SpanTrackEvent(ctx, "IsValid", fscache.GetAttributesForFuncSvc(fsvc)...)
	for _, obj := range fsvc.KubernetesObjects {
		if strings.ToLower(obj.Kind) == "pod" {
			pod := &apiv1.Pod{}
			err := gpm.crClient.Get(ctx, client.ObjectKey{Namespace: obj.Namespace, Name: obj.Name}, pod)
			if err == nil && utils.IsReadyPod(pod) {
				// Normally, the address format is http://[pod-ip]:[port], however, if the
				// Istio is enabled the address format changes to http://[svc-name]:[port].
				// So if the Istio is enabled and pod is in ready state, we return true directly;
				// Otherwise, we need to ensure that the address contains pod ip.
				if gpm.enableIstio ||
					(!gpm.enableIstio && strings.Contains(fsvc.Address, pod.Status.PodIP)) {
					gpm.logger.V(1).Info("valid address",
						"address", fsvc.Address,
						"function", fsvc.Function,
						"executor", string(fsvc.Executor),
					)
					return true
				}
			}
		}
	}
	return false
}

func (gpm *GenericPoolManager) RefreshFuncPods(ctx context.Context, logger logr.Logger, f fv1.Function) error {

	env, err := gpm.fissionClient.CoreV1().Environments(f.Spec.Environment.Namespace).Get(ctx, f.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	gp, created, err := gpm.getPool(ctx, env, nil)
	if err != nil {
		return err
	}

	if created {
		gpm.logger.Info("created pool for the environment", "env", env.Name, "namespace", gpm.nsResolver.ResolveNamespace(gpm.nsResolver.FunctionNamespace))
	}

	funcSvc, err := gp.fsCache.GetByFunction(&f.ObjectMeta)

	// delete function service address from cache only when function service address found in cache
	if err == nil {
		gp.fsCache.DeleteEntry(funcSvc)
	}

	funcLabels := gp.labelsForFunction(&f.ObjectMeta)

	podList, err := gpm.kubernetesClient.CoreV1().Pods(f.Spec.Environment.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(funcLabels).AsSelector().String(),
	})

	if err != nil {
		return err
	}

	for _, po := range podList.Items {
		err := gpm.kubernetesClient.CoreV1().Pods(po.ObjectMeta.Namespace).Delete(ctx, po.Name, metav1.DeleteOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}

	return nil
}

func (gpm *GenericPoolManager) AdoptExistingResources(ctx context.Context) {
	wg := &sync.WaitGroup{}

	envMap := gpm.adoptPools(ctx, wg)
	gpm.adoptPerImagePoolDeployments(ctx, wg)
	gpm.adoptFunctionServices(ctx, wg)
	gpm.adoptSpecializedPods(ctx, wg, envMap)

	wg.Wait()
}

// adoptFunctionServices re-stamps the instanceID annotation of the per-function
// headless Services (RFC-0002) so the post-adopt stale-instanceID reaper keeps
// them. Like per-image pool deployments, they are created lazily on first
// invoke, so nothing else refreshes their annotation across an executor restart.
func (gpm *GenericPoolManager) adoptFunctionServices(ctx context.Context, wg *sync.WaitGroup) {
	selector := labels.Set(map[string]string{
		fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE,
		fv1.EXECUTOR_TYPE:    string(fv1.ExecutorTypePoolmgr),
	}).AsSelector().String()
	for _, namespace := range gpm.nsResolver.FunctionNamespaces() {
		svcList, err := gpm.kubernetesClient.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			gpm.logger.Error(err, "error listing function services for adoption", "namespace", namespace)
			continue
		}
		for i := range svcList.Items {
			svc := &svcList.Items[i]
			wg.Go(func() {
				patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s"}}}`, fv1.EXECUTOR_INSTANCEID_LABEL, gpm.instanceID)
				_, err := gpm.kubernetesClient.CoreV1().Services(svc.Namespace).Patch(ctx, svc.Name, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
				if err != nil {
					gpm.logger.Error(err, "error adopting function service", "service", svc.Name, "ns", svc.Namespace)
				}
			})
		}
	}
}

// adoptPools re-creates (and thereby re-stamps) each environment's plain warm
// pool, and returns the env map keyed by namespace/name for the specialized-pod
// adoption pass.
func (gpm *GenericPoolManager) adoptPools(ctx context.Context, wg *sync.WaitGroup) map[string]fv1.Environment {
	envMap := make(map[string]fv1.Environment)

	for _, namespace := range utils.DefaultNSResolver().FissionResourceNamespaces() {
		envs, err := gpm.fissionClient.CoreV1().Environments(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			gpm.logger.Error(err, "error getting environment list")
			return envMap
		}

		for i := range envs.Items {
			env := envs.Items[i]

			if getEnvPoolSize(&env) > 0 {
				wg.Go(func() {
					_, created, err := gpm.getPool(ctx, &env, nil)
					if err != nil {
						gpm.logger.Error(err, "adopt pool failed")
					}
					if created {
						gpm.logger.Info("created pool for the environment", "env", env.Name, "namespace", gpm.nsResolver.ResolveNamespace(gpm.nsResolver.FunctionNamespace))
					}
				})
			}

			// create environment map for later use
			key := k8sCache.MetaObjectToName(&env.ObjectMeta).String()
			envMap[key] = env
		}
	}
	return envMap
}

// adoptPerImagePoolDeployments re-stamps the instanceID annotation of per-image
// (RFC-0001 Path B) pool deployments. They are created lazily on the first
// request, so adoptPools (which adopts each env's plain pool via getPool) never
// refreshes their annotation — and the post-adopt reaper deletes any poolmgr
// deployment with a stale instanceID. Adopt them in place here; the pool object
// re-attaches to the deployment on the next request for its image.
func (gpm *GenericPoolManager) adoptPerImagePoolDeployments(ctx context.Context, wg *sync.WaitGroup) {
	l := map[string]string{
		fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr),
	}

	perImageSelector := labels.Set(l).AsSelector().String() + "," + fv1.POOL_OCI_IMAGE_HASH
	for _, namespace := range gpm.nsResolver.FunctionNamespaces() {
		deployList, err := gpm.kubernetesClient.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: perImageSelector,
		})
		if err != nil {
			gpm.logger.Error(err, "error listing per-image pool deployments for adoption", "namespace", namespace)
			continue
		}
		for i := range deployList.Items {
			depl := &deployList.Items[i]
			wg.Go(func() {
				patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s"}}}`, fv1.EXECUTOR_INSTANCEID_LABEL, gpm.instanceID)
				_, err := gpm.kubernetesClient.AppsV1().Deployments(depl.Namespace).Patch(ctx, depl.Name, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
				if err != nil {
					gpm.logger.Error(err, "error adopting per-image pool deployment", "deployment", depl.Name, "ns", depl.Namespace)
				}
			})
		}
	}
}

// adoptSpecializedPods re-stamps every ready poolmgr pod's instanceID annotation
// and re-registers specialized pods (managed=false) into the fsCache from their
// labels/annotations, so functions keep being served across an executor restart.
func (gpm *GenericPoolManager) adoptSpecializedPods(ctx context.Context, wg *sync.WaitGroup, envMap map[string]fv1.Environment) {
	l := map[string]string{
		fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr),
	}

	for _, namespace := range utils.DefaultNSResolver().FissionResourceNamespaces() {
		podList, err := gpm.kubernetesClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.Set(l).AsSelector().String(),
		})

		if err != nil {
			gpm.logger.Error(err, "error getting pod list")
			return
		}

		for i := range podList.Items {
			pod := &podList.Items[i]
			if !utils.IsReadyPod(pod) {
				continue
			}

			wg.Go(func() {
				// avoid too many requests arrive Kubernetes API server at the same time.
				time.Sleep(time.Duration(rand.Intn(30)) * time.Millisecond)

				patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s"}}}`, fv1.EXECUTOR_INSTANCEID_LABEL, gpm.instanceID)
				// Locals, not the loop-level pod/err: these goroutines run
				// concurrently and writes to shared captures would race.
				pod, perr := gpm.kubernetesClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
				if perr != nil {
					// just log the error since it won't affect the function serving
					gpm.logger.Error(perr, "error patching executor instance ID of pod", "pod", pod.Name, "ns", pod.Namespace)
					return
				}

				// for unspecialized pod, we only update its annotations
				if pod.Labels["managed"] == "true" {
					return
				}

				fnName, ok1 := pod.Labels[fv1.FUNCTION_NAME]
				fnNS, ok2 := pod.Labels[fv1.FUNCTION_NAMESPACE]
				fnUID, ok3 := pod.Labels[fv1.FUNCTION_UID]
				fnRV, ok4 := pod.Annotations[fv1.FUNCTION_RESOURCE_VERSION]
				envName, ok5 := pod.Labels[fv1.ENVIRONMENT_NAME]
				envNS, ok6 := pod.Labels[fv1.ENVIRONMENT_NAMESPACE]
				svcHost, ok7 := pod.Annotations[fv1.ANNOTATION_SVC_HOST]
				env, ok8 := envMap[fmt.Sprintf("%s/%s", envNS, envName)]

				if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !ok7 || !ok8 {
					gpm.logger.Info("failed to adopt pod for function due to lack of necessary information",
						"pod", pod.Name, "labels", pod.Labels, "annotations", pod.Annotations,
						"env", env.Name)
					return
				}

				fsvc := fscache.FuncSvc{
					Name: pod.Name,
					Function: &metav1.ObjectMeta{
						Name:            fnName,
						Namespace:       fnNS,
						UID:             k8sTypes.UID(fnUID),
						ResourceVersion: fnRV,
					},
					Environment: &env,
					Address:     svcHost,
					KubernetesObjects: []apiv1.ObjectReference{
						{
							Kind:            "pod",
							Name:            pod.Name,
							APIVersion:      pod.APIVersion,
							Namespace:       pod.Namespace,
							ResourceVersion: pod.ResourceVersion,
							UID:             pod.UID,
						},
					},
					Executor: fv1.ExecutorTypePoolmgr,
					Ctime:    time.Now(),
					Atime:    time.Now(),
				}

				if _, aerr := gpm.fsCache.Add(fsvc); aerr != nil {
					// If fsvc already exists we just skip the duplicate one. And let reaper to recycle the duplicate pods.
					// This is for the case that there are multiple function pods for the same function due to unknown reason.
					if !fscache.IsNameExistError(aerr) {
						gpm.logger.Error(aerr, "failed to adopt pod for function", "pod", pod.Name)
					}

					return
				}

				gpm.logger.Info("adopt function pod",
					"pod", pod.Name, "labels", pod.Labels, "annotations", pod.Annotations)
			})
		}
	}
}

func (gpm *GenericPoolManager) CleanupOldExecutorObjects(ctx context.Context) {
	gpm.logger.Info("Poolmanager starts to clean orphaned resources", "instanceID", gpm.instanceID)

	var errs error
	listOpts := metav1.ListOptions{
		LabelSelector: labels.Set(map[string]string{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr)}).AsSelector().String(),
	}

	err := reaper.CleanupDeployments(ctx, gpm.logger, gpm.kubernetesClient, gpm.instanceID, listOpts)
	if err != nil {
		errs = errors.Join(errs, err)
	}

	err = reaper.CleanupPods(ctx, gpm.logger, gpm.kubernetesClient, gpm.instanceID, listOpts)
	if err != nil {
		errs = errors.Join(errs, err)
	}

	// Per-function headless Services (RFC-0002). The selector includes
	// managed-by so the legacy useSvc/istio Services (which carry no
	// instanceID annotation and are skipped by CleanupServices anyway) are
	// never even listed.
	fnSvcListOpts := metav1.ListOptions{
		LabelSelector: labels.Set(map[string]string{
			fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE,
			fv1.EXECUTOR_TYPE:    string(fv1.ExecutorTypePoolmgr),
		}).AsSelector().String(),
	}
	err = reaper.CleanupServices(ctx, gpm.logger, gpm.kubernetesClient, gpm.instanceID, fnSvcListOpts)
	if err != nil {
		errs = errors.Join(errs, err)
	}

	if errs != nil {
		// TODO retry reaper; logged and ignored for now
		gpm.logger.Error(errs, "Failed to cleanup old executor objects")
	}
}

// service is the pool-manager actor: it owns gpm.pools and serializes every
// access to it through gpm.requestChannel.
func (gpm *GenericPoolManager) service() {
	for {
		req := <-gpm.requestChannel
		switch req.requestType {
		case GET_POOL:
			gpm.handleGetPool(req)
		case CLEANUP_POOL:
			gpm.handleCleanupPool(req)
		case GET_ENV_POOLS:
			gpm.handleGetEnvPools(req)
		case REAP_IDLE_POOLS:
			gpm.handleReapIdlePools(req)
		}
	}
}

// handleGetPool returns the env's pool (plain, or per-image when req.oci is
// set), creating and seeding it on first use.
func (gpm *GenericPoolManager) handleGetPool(req *request) {
	// just because they are missing in the cache, we end up creating another duplicate pool.
	var err error
	created := false
	imageHash := ""
	if req.oci != nil {
		imageHash = ociPoolHash(req.oci)
	}
	key := poolKey(req.env.UID, imageHash)
	pool, ok := gpm.pools[key]
	if !ok {
		// To support backward compatibility, if envs are created in default ns, we go ahead
		// and create pools in fission-function ns as earlier.
		ns := gpm.nsResolver.GetFunctionNS(req.env.Namespace)
		pool = MakeGenericPool(gpm.logger, gpm.fissionClient, gpm.kubernetesClient,
			gpm.metricsClient, req.env, ns, gpm.fsCache,
			gpm.fetcherConfig, gpm.instanceID, gpm.enableIstio, gpm.podSpecPatch, gpm.crClient, req.oci,
			gpm.podReadyTimeout)
		err = pool.setup(req.ctx)
		if err != nil {
			req.responseChannel <- &response{error: err}
			return
		}
		gpm.pools[key] = pool
		// Publish the pool's readyPodQueue so the Pod reconciler can feed it
		// warm pods. Keyed by pool key, read lock-free from the reconciler.
		gpm.readyPodQueues.Store(key, pool.readyPodQueue)
		// Seed the queue with already-Running warm pods. The reconciler only
		// sees pods that change after the queue is published, so existing pods
		// (executor restart, or adopting an existing pool deployment) would
		// otherwise never be enqueued — mirrors the old informer's list-on-sync.
		// This is one-shot pool initialization (a fast cache read), not request
		// work, so it must not ride the request context: if the triggering
		// request is cancelled here the pool would stay published-but-unseeded
		// and later callers find the existing pool and skip the seed.
		gpm.seedReadyPodQueue(context.Background(), req.env, imageHash, pool.readyPodQueue)
		created = true
	}
	// Touch the pool's activity clock (every specialization starts with a
	// GET_POOL, and this handler is serialized with the reap pass, so a
	// just-touched pool can never be reaped out from under the request).
	pool.lastActive.Store(time.Now().UnixNano())
	req.responseChannel <- &response{pool: pool, created: created}
}

// handleCleanupPool destroys every pool an env owns: its plain pool plus any
// per-image pools (RFC-0001 Path B). The caller doesn't wait for a response.
func (gpm *GenericPoolManager) handleCleanupPool(req *request) {
	env := *req.env
	gpm.logger.Info("destroying pools",
		"environment", env.Name,
		"namespace", env.Namespace)

	found := false
	for key, pool := range gpm.pools {
		if !envPoolKeyPrefixMatch(key, req.env.UID) {
			continue
		}
		found = true
		delete(gpm.pools, key)
		gpm.readyPodQueues.Delete(key)
		if pool != nil {
			err := pool.destroy(req.ctx)
			if err != nil {
				gpm.logger.Error(err, "failed to destroy pool",
					"environment", env.Name,
					"namespace", env.Namespace,
					"poolKey", key)
			}
		}
	}
	if !found {
		gpm.logger.Info("pool already removed", "environment", env.Name, "namespace", env.Namespace)
	}
}

// handleGetEnvPools answers with every live pool of an env (plain + per-image).
func (gpm *GenericPoolManager) handleGetEnvPools(req *request) {
	pools := make([]*GenericPool, 0, 1)
	for key, pool := range gpm.pools {
		if envPoolKeyPrefixMatch(key, req.env.UID) {
			pools = append(pools, pool)
		}
	}
	req.responseChannel <- &response{pools: pools}
}

func (gpm *GenericPoolManager) getPool(ctx context.Context, env *fv1.Environment, oci *ociPoolSpec) (*GenericPool, bool, error) {
	otelUtils.SpanTrackEvent(ctx, "getPool", otelUtils.GetAttributesForEnv(env)...)
	c := make(chan *response)
	gpm.requestChannel <- &request{
		ctx:             ctx,
		requestType:     GET_POOL,
		env:             env,
		oci:             oci,
		responseChannel: c,
	}
	resp := <-c
	return resp.pool, resp.created, resp.error
}

// getEnvPools returns every live pool of an env: the plain pool plus any
// per-image pools (RFC-0001 Path B).
func (gpm *GenericPoolManager) getEnvPools(ctx context.Context, env *fv1.Environment) []*GenericPool {
	c := make(chan *response)
	gpm.requestChannel <- &request{
		ctx:             ctx,
		requestType:     GET_ENV_POOLS,
		env:             env,
		responseChannel: c,
	}
	resp := <-c
	return resp.pools
}

func (gpm *GenericPoolManager) cleanupPool(ctx context.Context, env *fv1.Environment) {
	otelUtils.SpanTrackEvent(ctx, "cleanupPool", otelUtils.GetAttributesForEnv(env)...)
	gpm.requestChannel <- &request{
		ctx:         ctx,
		requestType: CLEANUP_POOL,
		env:         env,
	}
}

// markFuncDeleted marks a function's pool service entries deleted in the fsCache
// so the idle reaper recycles its specialized pods. Driven by the Function
// reconciler on delete.
func (gpm *GenericPoolManager) markFuncDeleted(key crd.CacheKeyUG) {
	gpm.fsCache.MarkFuncDeleted(key)
	gpm.fnSvcEnsured.Delete(key.UID)
}

// processReplicaSet reaps a pool's specialized pods when its ReplicaSet has
// scaled to zero. Driven by the ReplicaSet reconciler; delegates to the pool pod
// controller, which owns the specialized-pod cleanup queue.
func (gpm *GenericPoolManager) processReplicaSet(ctx context.Context, rs *appsv1.ReplicaSet) {
	gpm.poolPodC.processRS(ctx, rs)
}

// readyPodEnqueueDelay debounces a warm pod's entry into its readyPodQueue.
// The 2021-era value was 100ms (#1890, an informer-settle guard with no stated
// derivation); today choosePod re-reads the pod from the cache and claims it
// with a verified relabel patch, and a pod dequeued too early lands on the
// not-ready requeue path (expoDelay backoff) and self-corrects — so the delay
// only needs to cover the common cache-settle case, not guarantee it. Every
// 10ms here is 10ms of pool-refill latency added to each queued cold start
// once the pool is exhausted (visible in the cold-burst benchmark scenarios).
const readyPodEnqueueDelay = 10 * time.Millisecond

// enqueueReadyPod adds a warm pod's key to its pool's readyPodQueue (looked up
// by pool key — env UID, plus image hash for per-image pools). Driven by the
// readyPod reconciler. A pool that no longer exists (race with destroy) is
// simply skipped — its queue is gone and choosePod won't run.
func (gpm *GenericPoolManager) enqueueReadyPod(queueKey, podKey string) {
	if q, ok := gpm.readyPodQueues.Load(queueKey); ok {
		q.(workqueue.TypedDelayingInterface[string]).AddAfter(podKey, readyPodEnqueueDelay)
	}
}

// seedReadyPodQueue enqueues an environment's already-Running warm pods into a
// freshly-published readyPodQueue, so choosePod isn't left blocked on an empty
// queue when pods were Running before the queue existed (executor restart, or
// adopting an existing pool deployment). The readyPodQueue dedups, so any overlap
// with the reconciler's own events is harmless. imageHash scopes the seed to
// one pool's pods: per-image pools select on their POOL_OCI_IMAGE_HASH label;
// the plain pool requires the label to be absent, so it never steals an
// image-volume pod (which has no fetcher and must not serve fetcher-path
// functions).
func (gpm *GenericPoolManager) seedReadyPodQueue(ctx context.Context, env *fv1.Environment, imageHash string, queue workqueue.TypedDelayingInterface[string]) {
	ns := gpm.nsResolver.GetFunctionNS(env.Namespace)
	selector := labels.SelectorFromSet(labels.Set{
		fv1.EXECUTOR_TYPE:   string(fv1.ExecutorTypePoolmgr),
		fv1.ENVIRONMENT_UID: string(env.UID),
		"managed":           "true",
	})
	if imageHash != "" {
		req, err := labels.NewRequirement(fv1.POOL_OCI_IMAGE_HASH, selection.Equals, []string{imageHash})
		if err != nil {
			gpm.logger.Error(err, "failed to build image-hash selector", "env", env.Name)
			return
		}
		selector = selector.Add(*req)
	} else {
		req, err := labels.NewRequirement(fv1.POOL_OCI_IMAGE_HASH, selection.DoesNotExist, nil)
		if err != nil {
			gpm.logger.Error(err, "failed to build image-hash selector", "env", env.Name)
			return
		}
		selector = selector.Add(*req)
	}
	podList := &apiv1.PodList{}
	if err := gpm.crClient.List(ctx, podList, client.InNamespace(ns),
		client.MatchingLabelsSelector{Selector: selector}); err != nil {
		gpm.logger.Error(err, "failed to seed ready pod queue", "env", env.Name, "namespace", env.Namespace)
		return
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.Phase != apiv1.PodRunning {
			continue
		}
		key, err := k8sCache.MetaNamespaceKeyFunc(pod)
		if err != nil {
			continue
		}
		queue.AddAfter(key, readyPodEnqueueDelay)
	}
}

// reconcileEnvPool brings an environment's warm pools to their desired state:
// ensure the plain pool exists, destroy every pool if the pool size is zero,
// otherwise update each pool deployment (the plain pool plus any per-image
// Path B pools). Driven by the Environment reconciler on create/update
// (replacing poolpodcontroller's envCreateUpdateQueue handler).
func (gpm *GenericPoolManager) reconcileEnvPool(ctx context.Context, env *fv1.Environment) error {
	log := gpm.logger.WithValues("env", env.Name, "namespace", env.Namespace)
	_, created, err := gpm.getPool(ctx, env, nil)
	if err != nil {
		return err
	}
	if created {
		log.Info("created pool for the environment")
		return nil
	}
	if getEnvPoolSize(env) == 0 {
		log.Info("pool size is zero, cleaning up pool")
		gpm.cleanupPool(ctx, env)
		return nil
	}
	var errs error
	for _, pool := range gpm.getEnvPools(ctx, env) {
		errs = errors.Join(errs, pool.updatePoolDeployment(ctx, env))
	}
	if errs != nil {
		return errs
	}
	// Any specialized pods are recycled by the ReplicaSet → cleanup path.
	return nil
}

// cleanupEnvPool destroys an environment's pool and reaps its specialized pods.
// Driven by the Environment reconciler on delete (replacing the envDeleteQueue
// handler); it uses the last-seen Environment since the live object is gone.
func (gpm *GenericPoolManager) cleanupEnvPool(ctx context.Context, env *fv1.Environment) {
	gpm.logger.V(1).Info("env delete: destroying pool and reaping specialized pods",
		"env", env.Name, "namespace", env.Namespace)
	gpm.cleanupPool(ctx, env)
	gpm.poolPodC.cleanupSpecializedPodsForEnv(ctx, env)
}

func (gpm *GenericPoolManager) getFunctionEnv(ctx context.Context, fn *fv1.Function) (*fv1.Environment, error) {
	var env *fv1.Environment
	otelUtils.SpanTrackEvent(ctx, "getFunctionEnv", otelUtils.GetAttributesForFunction(fn)...)

	// Defence in depth for GHSA-cvw6-gfvv-953q — the admission webhook
	// already rejects this at submit time, but a stale Function object
	// from an upgrade-before-webhook-restart window (or a cluster running
	// with failurePolicy=ignore) could still reach this path.
	if envNs := fn.Spec.Environment.Namespace; envNs != "" && envNs != fn.Namespace {
		return nil, fmt.Errorf("cross-namespace environment reference is not allowed: fn.namespace=%s env.namespace=%s",
			fn.Namespace, envNs)
	}

	// Cached ?
	// TODO: the cache should be able to search by <env name, fn namespace> instead of function metadata.
	result, err := gpm.functionEnv.Get(crd.CacheKeyURFromMeta(&fn.ObjectMeta))
	if err == nil {
		return result, nil
	}

	// Resolve the environment from the executor Manager cache.
	env = &fv1.Environment{}
	err = gpm.crClient.Get(ctx, client.ObjectKey{
		Namespace: fn.Spec.Environment.Namespace,
		Name:      fn.Spec.Environment.Name,
	}, env)
	if err != nil {
		return nil, err
	}

	// cache for future lookups
	m := fn.ObjectMeta
	_, err = gpm.functionEnv.Set(crd.CacheKeyURFromMeta(&m), env)
	if err != nil {
		gpm.logger.Error(err,
			"failed to set the key", "function", fn.Name,
		)
	}
	return env, nil
}

// IdleStrategy returns the poolmgr idle-reaping strategy (delete idle warm
// pods), run by the shared idle reaper.
func (gpm *GenericPoolManager) IdleStrategy() idle.Strategy {
	return idle.NewPoolDeleteStrategy(gpm.logger, gpm.fissionClient, gpm.fsCache, gpm.kubernetesClient,
		gpm.defaultIdlePodReapTime, gpm.objectReaperIntervalSecond, gpm.functionServicesEnabled)
}

// WebsocketStartEventChecker checks if the pod has emitted a websocket connection start event
func (gpm *GenericPoolManager) WebsocketStartEventChecker(ctx context.Context, kubeClient kubernetes.Interface) error {
	var wg wait.Group
	for _, informer := range utils.GetInformerEventChecker(ctx, kubeClient, "WsConnectionStarted") {
		_, err := informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				mObj := obj.(metav1.Object)
				gpm.logger.Info("Websocket event detected for pod",
					"Pod name", mObj.GetName())

				podName := strings.SplitAfter(mObj.GetName(), ".")
				if fsvc, ok := gpm.fsCache.PodToFsvc.Load(strings.TrimSuffix(podName[0], ".")); ok {
					fsvc, ok := fsvc.(*fscache.FuncSvc)
					if !ok {
						gpm.logger.Error(nil, "could not convert item from PodToFsvc")
						return
					}
					gpm.fsCache.WebsocketFsvc.Store(fsvc.Name, true)
				}
			},
		})
		if err != nil {
			return err
		}
		wg.StartWithChannel(ctx.Done(), informer.Run)
	}
	wg.Wait()
	return nil
}

// NoActiveConnectionEventChecker checks if the pod has emitted an inactive event
func (gpm *GenericPoolManager) NoActiveConnectionEventChecker(ctx context.Context, kubeClient kubernetes.Interface) error {
	var wg wait.Group
	for _, informer := range utils.GetInformerEventChecker(ctx, kubeClient, "NoActiveConnections") {
		_, err := informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				mObj := obj.(metav1.Object)
				gpm.logger.Info("Inactive event detected for pod",
					"Pod name", mObj.GetName())

				podName := strings.SplitAfter(mObj.GetName(), ".")
				if fsvc, ok := gpm.fsCache.PodToFsvc.Load(strings.TrimSuffix(podName[0], ".")); ok {
					fsvc, ok := fsvc.(*fscache.FuncSvc)
					if !ok {
						gpm.logger.Error(nil, "could not convert value from PodToFsvc")
						return
					}
					gpm.fsCache.DeleteFunctionSvc(ctx, fsvc)
					for i := range fsvc.KubernetesObjects {
						gpm.logger.Info("release idle function resources due to  inactivity",
							"function", fsvc.Function.Name,
							"address", fsvc.Address,
							"executor", string(fsvc.Executor),
							"pod", fsvc.Name,
						)
						reaper.CleanupKubeObject(ctx, gpm.logger, gpm.kubernetesClient, &fsvc.KubernetesObjects[i])
						time.Sleep(50 * time.Millisecond)
					}
				}

			},
		})
		if err != nil {
			return err
		}
		wg.StartWithChannel(ctx.Done(), informer.Run)
	}
	wg.Wait()
	return nil
}

func (gpm *GenericPoolManager) DumpDebugInfo(ctx context.Context) error {
	return gpm.fsCache.DumpDebugInfo(ctx)
}
