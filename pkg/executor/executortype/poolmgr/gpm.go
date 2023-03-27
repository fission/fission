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

package poolmgr

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/throttler"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sInformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	k8sCache "k8s.io/client-go/tools/cache"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/cache"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/reaper"
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
		logger *zap.Logger

		pools            map[string]*GenericPool
		kubernetesClient kubernetes.Interface
		metricsClient    metricsclient.Interface
		nsResolver       *utils.NamespaceResolver

		fissionClient  versioned.Interface
		functionEnv    *cache.Cache
		fsCache        *fscache.FunctionServiceCache
		throttler      *throttler.Throttler
		instanceID     string
		requestChannel chan *request

		enableIstio   bool
		fetcherConfig *fetcherConfig.Config

		// podLister can list/get pods from the shared informer's store
		podLister map[string]corelisters.PodLister

		// podListerSynced returns true if the pod store has been synced at least once.
		podListerSynced map[string]k8sCache.InformerSynced

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
	logger *zap.Logger,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	metricsClient metricsclient.Interface,
	fetcherConfig *fetcherConfig.Config,
	instanceID string,
	finformerFactory map[string]genInformer.SharedInformerFactory,
	gpmInformerFactory map[string]k8sInformers.SharedInformerFactory,
	podSpecPatch *apiv1.PodSpec,
) (executortype.ExecutorType, error) {

	gpmLogger := logger.Named("generic_pool_manager")

	enableIstio := false
	if len(os.Getenv("ENABLE_ISTIO")) > 0 {
		istio, err := strconv.ParseBool(os.Getenv("ENABLE_ISTIO"))
		if err != nil {
			gpmLogger.Error("failed to parse 'ENABLE_ISTIO', set to false", zap.Error(err))
		}
		enableIstio = istio
	}

	poolPodC := NewPoolPodController(ctx, gpmLogger, kubernetesClient,
		enableIstio, finformerFactory, gpmInformerFactory)

	gpm := &GenericPoolManager{
		logger:                     gpmLogger,
		pools:                      make(map[string]*GenericPool),
		kubernetesClient:           kubernetesClient,
		nsResolver:                 utils.DefaultNSResolver(),
		metricsClient:              metricsClient,
		fissionClient:              fissionClient,
		functionEnv:                cache.MakeCache(10*time.Second, 0),
		fsCache:                    fscache.MakeFunctionServiceCache(gpmLogger),
		throttler:                  throttler.MakeThrottler(time.Minute),
		instanceID:                 instanceID,
		requestChannel:             make(chan *request),
		defaultIdlePodReapTime:     2 * time.Minute,
		fetcherConfig:              fetcherConfig,
		enableIstio:                enableIstio,
		poolPodC:                   poolPodC,
		podSpecPatch:               podSpecPatch,
		objectReaperIntervalSecond: time.Duration(executorUtils.GetObjectReaperInterval(logger, fv1.ExecutorTypePoolmgr, 5)) * time.Second,
		podLister:                  make(map[string]corelisters.PodLister),
		podListerSynced:            make(map[string]k8sCache.InformerSynced),
	}
	for ns, informerFactory := range gpmInformerFactory {
		gpm.podLister[ns] = informerFactory.Core().V1().Pods().Lister()
		gpm.podListerSynced[ns] = informerFactory.Core().V1().Pods().Informer().HasSynced
	}

	gpm.logger.Debug("inside MakeGenericPoolManager")

	return gpm, nil
}

func (gpm *GenericPoolManager) Run(ctx context.Context) {
	waitSynced := make([]k8sCache.InformerSynced, 0)
	for _, podListerSynced := range gpm.podListerSynced {
		waitSynced = append(waitSynced, podListerSynced)
	}
	if ok := k8sCache.WaitForCacheSync(ctx.Done(), waitSynced...); !ok {
		gpm.logger.Fatal("failed to wait for caches to sync")
	}
	go gpm.service()
	gpm.poolPodC.InjectGpm(gpm)
	go gpm.WebsocketStartEventChecker(ctx, gpm.kubernetesClient)
	go gpm.NoActiveConnectionEventChecker(ctx, gpm.kubernetesClient)
	go gpm.idleObjectReaper(ctx)
	go gpm.poolPodC.Run(ctx, ctx.Done())
}

func (gpm *GenericPoolManager) GetTypeName(ctx context.Context) fv1.ExecutorType {
	return fv1.ExecutorTypePoolmgr
}

func (gpm *GenericPoolManager) GetFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, gpm.logger)
	if fn.Spec.OnceOnly {
		return gpm.getFuncSvc(ctx, fn)
	}

	svc, _, err := gpm.GetFuncSvcFromPoolCache(ctx, fn, fn.Spec.RequestsPerPod)
	if err == nil || ferror.IsTooManyRequests(err) {
		// direct return when get svc succeed or the error is too many requests
		return svc, err
	}

	var retryCount int
RETRY:
	// avoid specialize multi pods at the same time, because it will overflow the Concurrency
	fnSvcObj, err := gpm.throttler.RunOnceStrict(fn.GetName(), func(ableToCreate bool) (interface{}, error) {
		if ableToCreate {
			return gpm.getFuncSvc(ctx, fn)
		}

		svc, _, err := gpm.GetFuncSvcFromPoolCache(ctx, fn, fn.Spec.RequestsPerPod)
		return svc, err
	})
	if ferror.IsNotFound(err) && retryCount < 3 {
		// retry three times for the burst traffic
		retryCount++
		goto RETRY
	} else if err != nil {
		e := "error creating k8s resources for function"
		logger.Error(e,
			zap.Error(err),
			zap.String("function_name", fn.ObjectMeta.Name),
			zap.String("function_namespace", fn.ObjectMeta.Namespace))
		return nil, errors.Wrapf(err, "%s %s_%s", e, fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
	}

	fnSvc, ok := fnSvcObj.(*fscache.FuncSvc)
	if !ok {
		logger.Panic("receive unknown object while creating function - expected pointer of function service object")
	}

	otelUtils.SpanTrackEvent(ctx, "fnSvcResponse", fscache.GetAttributesForFuncSvc(fnSvc)...)
	return fnSvc, err
}

func (gpm *GenericPoolManager) getFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	otelUtils.SpanTrackEvent(ctx, "GetFuncSvc", otelUtils.GetAttributesForFunction(fn)...)
	logger := otelUtils.LoggerWithTraceID(ctx, gpm.logger)
	// from Func -> get Env
	logger.Debug("getting environment for function", zap.String("function", fn.ObjectMeta.Name))
	env, err := gpm.getFunctionEnv(ctx, fn)
	if err != nil {
		return nil, err
	}

	pool, created, err := gpm.getPool(ctx, env)
	if err != nil {
		return nil, err
	}

	if created {
		logger.Info("created pool for the environment", zap.String("env", env.ObjectMeta.Name), zap.String("namespace", gpm.nsResolver.ResolveNamespace(gpm.nsResolver.FunctionNamespace)))
	}

	// from GenericPool -> get one function container
	// (this also adds to the cache)
	logger.Debug("getting function service from pool", zap.String("function", fn.ObjectMeta.Name))
	return pool.getFuncSvc(ctx, fn)
}

func (gpm *GenericPoolManager) GetFuncSvcFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	return nil, nil
}

func (gpm *GenericPoolManager) GetFuncSvcFromPoolCache(ctx context.Context, fn *fv1.Function, requestsPerPod int) (*fscache.FuncSvc, int, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, gpm.logger)
	otelUtils.SpanTrackEvent(ctx, "GetFuncSvcFromPoolCache", otelUtils.GetAttributesForFunction(fn)...)

	concurrency := fn.Spec.Concurrency
	if concurrency == 0 {
		concurrency = 500
	}
	if requestsPerPod == 0 {
		requestsPerPod = 1
	}

	fnSvc, err := gpm.fsCache.GetFuncSvc(ctx, &fn.ObjectMeta, concurrency, requestsPerPod)
	if err != nil {
		return nil, 0, err
	} else if !gpm.IsValid(ctx, fnSvc) {
		logger.Debug("deleting cache entry for invalid address",
			zap.String("function_name", fn.ObjectMeta.Name),
			zap.String("function_namespace", fn.ObjectMeta.Namespace),
			zap.String("address", fnSvc.Address))
		gpm.DeleteFuncSvcFromCache(ctx, fnSvc)
		return fnSvc, 0, fmt.Errorf("deleting cache entry for invalid address: %v", fnSvc.Address)
	}

	return fnSvc, 0, nil
}

func (gpm *GenericPoolManager) DeleteFuncSvcFromCache(ctx context.Context, fsvc *fscache.FuncSvc) {
	otelUtils.SpanTrackEvent(ctx, "DeleteFuncSvcFromCache", fscache.GetAttributesForFuncSvc(fsvc)...)
	gpm.fsCache.DeleteFunctionSvc(ctx, fsvc)
}

func (gpm *GenericPoolManager) UnTapService(ctx context.Context, key string, svcHost string) {
	otelUtils.SpanTrackEvent(ctx, "UnTapService",
		attribute.KeyValue{Key: "key", Value: attribute.StringValue(key)},
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

// IsValid checks if pod is not deleted and that it has the address passed as the argument. Also checks that all the
// containers in it are reporting a ready status for the healthCheck.
func (gpm *GenericPoolManager) IsValid(ctx context.Context, fsvc *fscache.FuncSvc) bool {
	otelUtils.SpanTrackEvent(ctx, "IsValid", fscache.GetAttributesForFuncSvc(fsvc)...)
	for _, obj := range fsvc.KubernetesObjects {
		if strings.ToLower(obj.Kind) == "pod" {
			pod, err := gpm.podLister[obj.Namespace].Pods(obj.Namespace).Get(obj.Name)
			if err == nil && utils.IsReadyPod(pod) {
				// Normally, the address format is http://[pod-ip]:[port], however, if the
				// Istio is enabled the address format changes to http://[svc-name]:[port].
				// So if the Istio is enabled and pod is in ready state, we return true directly;
				// Otherwise, we need to ensure that the address contains pod ip.
				if gpm.enableIstio ||
					(!gpm.enableIstio && strings.Contains(fsvc.Address, pod.Status.PodIP)) {
					gpm.logger.Debug("valid address",
						zap.String("address", fsvc.Address),
						zap.Any("function", fsvc.Function),
						zap.String("executor", string(fsvc.Executor)),
					)
					return true
				}
			}
		}
	}
	return false
}

func (gpm *GenericPoolManager) RefreshFuncPods(ctx context.Context, logger *zap.Logger, f fv1.Function) error {

	env, err := gpm.fissionClient.CoreV1().Environments(f.Spec.Environment.Namespace).Get(ctx, f.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	gp, created, err := gpm.getPool(ctx, env)
	if err != nil {
		return err
	}

	if created {
		gpm.logger.Info("created pool for the environment", zap.String("env", env.ObjectMeta.Name), zap.String("namespace", gpm.nsResolver.ResolveNamespace(gpm.nsResolver.FunctionNamespace)))
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
		err := gpm.kubernetesClient.CoreV1().Pods(po.ObjectMeta.Namespace).Delete(ctx, po.ObjectMeta.Name, metav1.DeleteOptions{})
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
			gpm.logger.Error("error getting environment list", zap.Error(err))
			return
		}

		for i := range envs.Items {
			env := envs.Items[i]

			if getEnvPoolSize(&env) > 0 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, created, err := gpm.getPool(ctx, &env)
					if err != nil {
						gpm.logger.Error("adopt pool failed", zap.Error(err))
					}
					if created {
						gpm.logger.Info("created pool for the environment", zap.String("env", env.ObjectMeta.Name), zap.String("namespace", gpm.nsResolver.ResolveNamespace(gpm.nsResolver.FunctionNamespace)))
					}
				}()
			}

			// create environment map for later use
			key := fmt.Sprintf("%v/%v", env.ObjectMeta.Namespace, env.ObjectMeta.Name)
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
			gpm.logger.Error("error getting pod list", zap.Error(err))
			return
		}

		for i := range podList.Items {
			pod := &podList.Items[i]
			if !utils.IsReadyPod(pod) {
				continue
			}

			wg.Add(1)
			go func() {
				defer wg.Done()

				// avoid too many requests arrive Kubernetes API server at the same time.
				time.Sleep(time.Duration(rand.Intn(30)) * time.Millisecond)

				patch := fmt.Sprintf(`{"metadata":{"annotations":{"%v":"%v"}}}`, fv1.EXECUTOR_INSTANCEID_LABEL, gpm.instanceID)
				pod, err = gpm.kubernetesClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
				if err != nil {
					// just log the error since it won't affect the function serving
					gpm.logger.Warn("error patching executor instance ID of pod", zap.Error(err),
						zap.String("pod", pod.Name), zap.String("ns", pod.Namespace))
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
				env, ok8 := envMap[fmt.Sprintf("%v/%v", envNS, envName)]

				if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7 && ok8) {
					gpm.logger.Warn("failed to adopt pod for function due to lack of necessary information",
						zap.String("pod", pod.Name), zap.Any("labels", pod.Labels), zap.Any("annotations", pod.Annotations),
						zap.String("env", env.ObjectMeta.Name))
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
							APIVersion:      pod.TypeMeta.APIVersion,
							Namespace:       pod.ObjectMeta.Namespace,
							ResourceVersion: pod.ObjectMeta.ResourceVersion,
							UID:             pod.ObjectMeta.UID,
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
						gpm.logger.Warn("failed to adopt pod for function", zap.Error(err), zap.String("pod", pod.Name))
					}

					return
				}

				gpm.logger.Info("adopt function pod",
					zap.String("pod", pod.Name), zap.Any("labels", pod.Labels), zap.Any("annotations", pod.Annotations))
			}()
		}
	}

	wg.Wait()
}

func (gpm *GenericPoolManager) CleanupOldExecutorObjects(ctx context.Context) {
	gpm.logger.Info("Poolmanager starts to clean orphaned resources", zap.String("instanceID", gpm.instanceID))

	errs := &multierror.Error{}
	listOpts := metav1.ListOptions{
		LabelSelector: labels.Set(map[string]string{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr)}).AsSelector().String(),
	}

	err := reaper.CleanupDeployments(ctx, gpm.logger, gpm.kubernetesClient, gpm.instanceID, listOpts)
	if err != nil {
		errs = multierror.Append(errs, err)
	}

	err = reaper.CleanupPods(ctx, gpm.logger, gpm.kubernetesClient, gpm.instanceID, listOpts)
	if err != nil {
		errs = multierror.Append(errs, err)
	}

	if errs.ErrorOrNil() != nil {
		// TODO retry reaper; logged and ignored for now
		gpm.logger.Error("Failed to cleanup old executor objects", zap.Error(err))
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
			pool, ok := gpm.pools[crd.CacheKeyUID(&req.env.ObjectMeta)]
			if !ok {
				// To support backward compatibility, if envs are created in default ns, we go ahead
				// and create pools in fission-function ns as earlier.
				ns := gpm.nsResolver.GetFunctionNS(req.env.ObjectMeta.Namespace)
				pool = MakeGenericPool(gpm.logger, gpm.fissionClient, gpm.kubernetesClient,
					gpm.metricsClient, req.env, ns, gpm.fsCache,
					gpm.fetcherConfig, gpm.instanceID, gpm.enableIstio, gpm.podSpecPatch)
				err = pool.setup(req.ctx)
				if err != nil {
					req.responseChannel <- &response{error: err}
					continue
				}
				gpm.pools[crd.CacheKeyUID(&req.env.ObjectMeta)] = pool
				created = true
			}
			req.responseChannel <- &response{pool: pool, created: created}
		case CLEANUP_POOL:
			env := *req.env
			gpm.logger.Info("destroying pool",
				zap.String("environment", env.ObjectMeta.Name),
				zap.String("namespace", env.ObjectMeta.Namespace))

			key := crd.CacheKeyUID(&req.env.ObjectMeta)
			pool, ok := gpm.pools[key]
			if !ok {
				gpm.logger.Error("Could not find pool", zap.String("environment", env.ObjectMeta.Name), zap.String("namespace", env.ObjectMeta.Namespace))
				return
			}
			delete(gpm.pools, key)
			err := pool.destroy(req.ctx)
			if err != nil {
				gpm.logger.Error("failed to destroy pool",
					zap.String("environment", env.ObjectMeta.Name),
					zap.String("namespace", env.ObjectMeta.Namespace),
					zap.Error(err))
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

func (gpm *GenericPoolManager) getFunctionEnv(ctx context.Context, fn *fv1.Function) (*fv1.Environment, error) {
	var env *fv1.Environment
	otelUtils.SpanTrackEvent(ctx, "getFunctionEnv", otelUtils.GetAttributesForFunction(fn)...)

	// Cached ?
	// TODO: the cache should be able to search by <env name, fn namespace> instead of function metadata.
	result, err := gpm.functionEnv.Get(crd.CacheKey(&fn.ObjectMeta))
	if err == nil {
		env = result.(*fv1.Environment)
		return env, nil
	}

	// Get env from controller
	envLister, err := gpm.poolPodC.getEnvLister(fn.Spec.Environment.Namespace)
	if err != nil {
		return nil, err
	}
	env, err = envLister.Environments(fn.Spec.Environment.Namespace).Get(fn.Spec.Environment.Name)
	if err != nil {
		return nil, err
	}

	// cache for future lookups
	m := fn.ObjectMeta
	_, err = gpm.functionEnv.Set(crd.CacheKey(&m), env)
	if err != nil {
		gpm.logger.Error(
			"failed to set the key",
			zap.String("function", fn.Name),
			zap.Error(err),
		)
	}
	return env, nil
}

// idleObjectReaper reaps objects after certain idle time
func (gpm *GenericPoolManager) idleObjectReaper(ctx context.Context) {
	// calling function doIdleObjectReaper() repeatedly at given interval of time
	wait.UntilWithContext(ctx, gpm.doIdleObjectReaper, gpm.objectReaperIntervalSecond)
}

func (gpm *GenericPoolManager) doIdleObjectReaper(ctx context.Context) {
	envList := make(map[k8sTypes.UID]struct{})
	for _, namespace := range utils.DefaultNSResolver().FissionResourceNS {
		envs, err := gpm.fissionClient.CoreV1().Environments(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			gpm.logger.Error("failed to get environment list", zap.Error(err), zap.String("namespace", namespace))
			return
		}

		for _, env := range envs.Items {
			envList[env.ObjectMeta.UID] = struct{}{}
		}
	}

	fnList := make(map[k8sTypes.UID]fv1.Function)
	for _, namespace := range utils.DefaultNSResolver().FissionResourceNS {
		fns, err := gpm.fissionClient.CoreV1().Functions(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			gpm.logger.Error("failed to get environment list", zap.Error(err), zap.String("namespace", namespace))
			return
		}
		for i, fn := range fns.Items {
			fnList[fn.ObjectMeta.UID] = fns.Items[i]
		}
	}

	funcSvcs, err := gpm.fsCache.ListOldForPool(time.Second * 5)
	if err != nil {
		gpm.logger.Error("error reaping idle pods", zap.Error(err))
		return
	}

	for i := range funcSvcs {
		fsvc := funcSvcs[i]

		if fsvc.Executor != fv1.ExecutorTypePoolmgr {
			continue
		}

		if _, ok := gpm.fsCache.WebsocketFsvc.Load(fsvc.Name); ok {
			continue
		}
		// For function with the environment that no longer exists, executor
		// cleanups the idle pod as usual and prints log to notify user.
		if _, ok := envList[fsvc.Environment.ObjectMeta.UID]; !ok {
			gpm.logger.Warn("function environment no longer exists",
				zap.String("environment", fsvc.Environment.ObjectMeta.Name),
				zap.String("function", fsvc.Name))
		}

		if fsvc.Environment.Spec.AllowedFunctionsPerContainer == fv1.AllowedFunctionsPerContainerInfinite {
			continue
		}

		idlePodReapTime := gpm.defaultIdlePodReapTime
		if fn, ok := fnList[fsvc.Function.UID]; ok {
			if fn.Spec.IdleTimeout != nil {
				idlePodReapTime = time.Duration(*fn.Spec.IdleTimeout) * time.Second
			}
		}

		if time.Since(fsvc.Atime) < idlePodReapTime {
			continue
		}

		go func() {
			deleted, err := gpm.fsCache.DeleteOldPoolCache(ctx, fsvc, idlePodReapTime)
			if err != nil {
				gpm.logger.Error("error deleting Kubernetes objects for function service",
					zap.Error(err),
					zap.Any("service", fsvc))
			}
			if deleted {
				for i := range fsvc.KubernetesObjects {
					gpm.logger.Info("release idle function resources",
						zap.String("function", fsvc.Function.Name),
						zap.String("address", fsvc.Address),
						zap.String("executor", string(fsvc.Executor)),
						zap.String("pod", fsvc.Name),
					)
					reaper.CleanupKubeObject(ctx, gpm.logger, gpm.kubernetesClient, &fsvc.KubernetesObjects[i])
					time.Sleep(50 * time.Millisecond)
				}
			}
		}()
	}
}

// WebsocketStartEventChecker checks if the pod has emitted a websocket connection start event
func (gpm *GenericPoolManager) WebsocketStartEventChecker(ctx context.Context, kubeClient kubernetes.Interface) {
	stopper := make(chan struct{})
	defer close(stopper)

	var wg wait.Group
	for _, informer := range utils.GetInformerEventChecker(ctx, kubeClient, "WsConnectionStarted") {
		informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				mObj := obj.(metav1.Object)
				gpm.logger.Info("Websocket event detected for pod",
					zap.String("Pod name", mObj.GetName()))

				podName := strings.SplitAfter(mObj.GetName(), ".")
				if fsvc, ok := gpm.fsCache.PodToFsvc.Load(strings.TrimSuffix(podName[0], ".")); ok {
					fsvc, ok := fsvc.(*fscache.FuncSvc)
					if !ok {
						gpm.logger.Error("could not convert item from PodToFsvc")
						return
					}
					gpm.fsCache.WebsocketFsvc.Store(fsvc.Name, true)
				}
			},
		})
		wg.StartWithChannel(stopper, informer.Run)
	}
	wg.Wait()
}

// NoActiveConnectionEventChecker checks if the pod has emitted an inactive event
func (gpm *GenericPoolManager) NoActiveConnectionEventChecker(ctx context.Context, kubeClient kubernetes.Interface) {
	stopper := make(chan struct{})
	defer close(stopper)

	var wg wait.Group
	for _, informer := range utils.GetInformerEventChecker(ctx, kubeClient, "WsConnectionStarted") {
		informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				mObj := obj.(metav1.Object)
				gpm.logger.Info("Inactive event detected for pod",
					zap.String("Pod name", mObj.GetName()))

				podName := strings.SplitAfter(mObj.GetName(), ".")
				if fsvc, ok := gpm.fsCache.PodToFsvc.Load(strings.TrimSuffix(podName[0], ".")); ok {
					fsvc, ok := fsvc.(*fscache.FuncSvc)
					if !ok {
						gpm.logger.Error("could not convert value from PodToFsvc")
						return
					}
					gpm.fsCache.DeleteFunctionSvc(ctx, fsvc)
					for i := range fsvc.KubernetesObjects {
						gpm.logger.Info("release idle function resources due to  inactivity",
							zap.String("function", fsvc.Function.Name),
							zap.String("address", fsvc.Address),
							zap.String("executor", string(fsvc.Executor)),
							zap.String("pod", fsvc.Name),
						)
						reaper.CleanupKubeObject(ctx, gpm.logger, gpm.kubernetesClient, &fsvc.KubernetesObjects[i])
						time.Sleep(50 * time.Millisecond)
					}
				}

			},
		})
		wg.StartWithChannel(stopper, informer.Run)
	}
	wg.Wait()
}
