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
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/metrics"
	"github.com/fission/fission/pkg/executor/reaper"
	"github.com/fission/fission/pkg/executor/reaper/idle"
	executorUtils "github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
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
)

type (
	GenericPoolManager struct {
		logger logr.Logger

		pools            map[k8sTypes.UID]*GenericPool
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

		// readyPodQueues maps env UID -> a pool's readyPodQueue, published by the
		// actor on pool create and removed on destroy. The Pod reconciler reads it
		// lock-free (sync.Map) to feed warm pods into the right pool's queue.
		readyPodQueues sync.Map

		enableIstio   bool
		fetcherConfig *fetcherConfig.Config

		defaultIdlePodReapTime time.Duration

		poolPodC *PoolPodController

		podSpecPatch               *apiv1.PodSpec
		objectReaperIntervalSecond time.Duration
	}
	request struct {
		requestType
		ctx             context.Context
		env             *fv1.Environment
		responseChannel chan *response
	}
	response struct {
		error
		pool    *GenericPool
		created bool
	}
)

func MakeGenericPoolManager(ctx context.Context,
	logger logr.Logger,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	metricsClient metricsclient.Interface,
	fetcherConfig *fetcherConfig.Config,
	instanceID string,
	finformerFactory map[string]genInformer.SharedInformerFactory,
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

	poolPodC := NewPoolPodController(gpmLogger, kubernetesClient)
	gpm := &GenericPoolManager{
		logger:                     gpmLogger,
		pools:                      make(map[k8sTypes.UID]*GenericPool),
		kubernetesClient:           kubernetesClient,
		nsResolver:                 utils.DefaultNSResolver(),
		metricsClient:              metricsClient,
		fissionClient:              fissionClient,
		functionEnv:                cache.MakeCache[crd.CacheKeyUR, *fv1.Environment](10*time.Second, 0),
		fsCache:                    fscache.MakeFunctionServiceCache(gpmLogger),
		instanceID:                 instanceID,
		requestChannel:             make(chan *request),
		defaultIdlePodReapTime:     2 * time.Minute,
		fetcherConfig:              fetcherConfig,
		enableIstio:                enableIstio,
		poolPodC:                   poolPodC,
		podSpecPatch:               podSpecPatch,
		objectReaperIntervalSecond: time.Duration(executorUtils.GetObjectReaperInterval(logger, fv1.ExecutorTypePoolmgr, 5)) * time.Second,
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
}

func (gpm *GenericPoolManager) GetTypeName(ctx context.Context) fv1.ExecutorType {
	return fv1.ExecutorTypePoolmgr
}

func (gpm *GenericPoolManager) GetFuncSvc(ctx context.Context, fn *fv1.Function) (fnSvc *fscache.FuncSvc, fErr error) {
	defer func() {
		if fErr != nil {
			metrics.ColdStartsError.WithLabelValues(fn.Name, fn.Namespace).Inc()
			return
		}

		metrics.ColdStarts.WithLabelValues(fn.Name, fn.Namespace).Inc()
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

	pool, created, err := gpm.getPool(ctx, env)
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
	return fnSvc, fErr
}

func (gpm *GenericPoolManager) GetFuncSvcFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	otelUtils.SpanTrackEvent(ctx, "GetFuncSvcFromCache", otelUtils.GetAttributesForFunction(fn)...)
	return gpm.fsCache.GetFuncSvc(ctx, &fn.ObjectMeta, fn.GetRequestPerPod(), fn.GetConcurrency())
}

func (gpm *GenericPoolManager) DeleteFuncSvcFromCache(ctx context.Context, fsvc *fscache.FuncSvc) {
	otelUtils.SpanTrackEvent(ctx, "DeleteFuncSvcFromCache", fscache.GetAttributesForFuncSvc(fsvc)...)
	gpm.fsCache.DeleteFunctionSvc(ctx, fsvc)
}

func (gpm *GenericPoolManager) UnTapService(ctx context.Context, fnMeta *metav1.ObjectMeta, svcHost string) {
	key := crd.CacheKeyURGFromMeta(fnMeta)
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
	key := crd.CacheKeyURGFromMeta(fnMeta)
	otelUtils.SpanTrackEvent(ctx, "MarkSpecializationFailure",
		attribute.KeyValue{Key: "key", Value: attribute.StringValue(key.String())})
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

	gp, created, err := gpm.getPool(ctx, env)
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
	envMap := make(map[string]fv1.Environment)
	wg := &sync.WaitGroup{}

	for _, namespace := range utils.DefaultNSResolver().FissionResourceNS {
		envs, err := gpm.fissionClient.CoreV1().Environments(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			gpm.logger.Error(err, "error getting environment list")
			return
		}

		for i := range envs.Items {
			env := envs.Items[i]

			if getEnvPoolSize(&env) > 0 {
				wg.Go(func() {
					_, created, err := gpm.getPool(ctx, &env)
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

	l := map[string]string{
		fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr),
	}

	for _, namespace := range utils.DefaultNSResolver().FissionResourceNS {
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
				pod, err = gpm.kubernetesClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
				if err != nil {
					// just log the error since it won't affect the function serving
					gpm.logger.Error(err, "error patching executor instance ID of pod", "pod", pod.Name, "ns", pod.Namespace)
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

				_, err = gpm.fsCache.Add(fsvc)
				if err != nil {
					// If fsvc already exists we just skip the duplicate one. And let reaper to recycle the duplicate pods.
					// This is for the case that there are multiple function pods for the same function due to unknown reason.
					if !fscache.IsNameExistError(err) {
						gpm.logger.Error(err, "failed to adopt pod for function", "pod", pod.Name)
					}

					return
				}

				gpm.logger.Info("adopt function pod",
					"pod", pod.Name, "labels", pod.Labels, "annotations", pod.Annotations)
			})
		}
	}

	wg.Wait()
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

	if errs != nil {
		// TODO retry reaper; logged and ignored for now
		gpm.logger.Error(err, "Failed to cleanup old executor objects")
	}
}

func (gpm *GenericPoolManager) service() {
	for {
		req := <-gpm.requestChannel
		switch req.requestType {
		case GET_POOL:
			// just because they are missing in the cache, we end up creating another duplicate pool.
			var err error
			created := false
			pool, ok := gpm.pools[crd.CacheKeyUIDFromMeta(&req.env.ObjectMeta)]
			if !ok {
				// To support backward compatibility, if envs are created in default ns, we go ahead
				// and create pools in fission-function ns as earlier.
				ns := gpm.nsResolver.GetFunctionNS(req.env.Namespace)
				pool = MakeGenericPool(gpm.logger, gpm.fissionClient, gpm.kubernetesClient,
					gpm.metricsClient, req.env, ns, gpm.fsCache,
					gpm.fetcherConfig, gpm.instanceID, gpm.enableIstio, gpm.podSpecPatch, gpm.crClient)
				err = pool.setup(req.ctx)
				if err != nil {
					req.responseChannel <- &response{error: err}
					continue
				}
				gpm.pools[crd.CacheKeyUIDFromMeta(&req.env.ObjectMeta)] = pool
				// Publish the pool's readyPodQueue so the Pod reconciler can feed it
				// warm pods. Keyed by env UID, read lock-free from the reconciler.
				gpm.readyPodQueues.Store(string(req.env.UID), pool.readyPodQueue)
				created = true
			}
			req.responseChannel <- &response{pool: pool, created: created}
		case CLEANUP_POOL:
			env := *req.env
			gpm.logger.Info("destroying pool",
				"environment", env.Name,
				"namespace", env.Namespace)

			key := crd.CacheKeyUIDFromMeta(&req.env.ObjectMeta)
			pool, ok := gpm.pools[key]
			if !ok {
				gpm.logger.Error(nil, "Could not find pool", "environment", env.Name, "namespace", env.Namespace)
				continue
			}
			delete(gpm.pools, key)
			gpm.readyPodQueues.Delete(string(req.env.UID))
			if pool != nil {
				err := pool.destroy(req.ctx)
				if err != nil {
					gpm.logger.Error(err, "failed to destroy pool",
						"environment", env.Name,
						"namespace", env.Namespace)
				}
			}
			// no response, caller doesn't wait
		}
	}
}

func (gpm *GenericPoolManager) getPool(ctx context.Context, env *fv1.Environment) (*GenericPool, bool, error) {
	otelUtils.SpanTrackEvent(ctx, "getPool", otelUtils.GetAttributesForEnv(env)...)
	c := make(chan *response)
	gpm.requestChannel <- &request{
		ctx:             ctx,
		requestType:     GET_POOL,
		env:             env,
		responseChannel: c,
	}
	resp := <-c
	return resp.pool, resp.created, resp.error
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
func (gpm *GenericPoolManager) markFuncDeleted(key crd.CacheKeyURG) {
	gpm.fsCache.MarkFuncDeleted(key)
}

// processReplicaSet reaps a pool's specialized pods when its ReplicaSet has
// scaled to zero. Driven by the ReplicaSet reconciler; delegates to the pool pod
// controller, which owns the specialized-pod cleanup queue.
func (gpm *GenericPoolManager) processReplicaSet(ctx context.Context, rs *appsv1.ReplicaSet) {
	gpm.poolPodC.processRS(ctx, rs)
}

// enqueueReadyPod adds a warm pod's key to its pool's readyPodQueue (looked up by
// env UID). Driven by the readyPod reconciler. A pool that no longer exists (race
// with destroy) is simply skipped — its queue is gone and choosePod won't run.
func (gpm *GenericPoolManager) enqueueReadyPod(envUID, key string) {
	if q, ok := gpm.readyPodQueues.Load(envUID); ok {
		q.(workqueue.TypedDelayingInterface[string]).AddAfter(key, 100*time.Millisecond)
	}
}

// reconcileEnvPool brings an environment's warm pool to its desired state:
// ensure the pool exists, destroy it if the pool size is zero, otherwise update
// the pool deployment. Driven by the Environment reconciler on create/update
// (replacing poolpodcontroller's envCreateUpdateQueue handler).
func (gpm *GenericPoolManager) reconcileEnvPool(ctx context.Context, env *fv1.Environment) error {
	log := gpm.logger.WithValues("env", env.Name, "namespace", env.Namespace)
	pool, created, err := gpm.getPool(ctx, env)
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
	if err := pool.updatePoolDeployment(ctx, env); err != nil {
		return err
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
		gpm.defaultIdlePodReapTime, gpm.objectReaperIntervalSecond)
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
