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

	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/cache"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/reaper"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	finformerv1 "github.com/fission/fission/pkg/generated/informers/externalversions/core/v1"
	"github.com/fission/fission/pkg/utils"
)

var _ executortype.ExecutorType = &GenericPoolManager{}

type requestType int

const (
	GET_POOL requestType = iota
	CLEANUP_POOL
)

type (
	GenericPoolManager struct {
		logger *zap.Logger

		pools            map[string]*GenericPool
		kubernetesClient *kubernetes.Clientset
		metricsClient    *metricsclient.Clientset
		namespace        string

		fissionClient  *crd.FissionClient
		functionEnv    *cache.Cache
		fsCache        *fscache.FunctionServiceCache
		instanceID     string
		requestChannel chan *request

		enableIstio   bool
		fetcherConfig *fetcherConfig.Config

		podInformer k8sCache.SharedIndexInformer

		defaultIdlePodReapTime time.Duration

		poolPodC *PoolPodController
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

func MakeGenericPoolManager(
	logger *zap.Logger,
	fissionClient *crd.FissionClient,
	kubernetesClient *kubernetes.Clientset,
	metricsClient *metricsclient.Clientset,
	functionNamespace string,
	fetcherConfig *fetcherConfig.Config,
	instanceID string,
	funcInformer finformerv1.FunctionInformer,
	pkgInformer finformerv1.PackageInformer,
	envInformer finformerv1.EnvironmentInformer,
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

	poolPodC := NewPoolPodController(gpmLogger, kubernetesClient, functionNamespace,
		enableIstio, funcInformer, pkgInformer, envInformer)

	gpm := &GenericPoolManager{
		logger:                 gpmLogger,
		pools:                  make(map[string]*GenericPool),
		kubernetesClient:       kubernetesClient,
		metricsClient:          metricsClient,
		namespace:              functionNamespace,
		fissionClient:          fissionClient,
		functionEnv:            cache.MakeCache(10*time.Second, 0),
		fsCache:                fscache.MakeFunctionServiceCache(gpmLogger),
		instanceID:             instanceID,
		requestChannel:         make(chan *request),
		defaultIdlePodReapTime: 2 * time.Minute,
		fetcherConfig:          fetcherConfig,
		enableIstio:            enableIstio,
		poolPodC:               poolPodC,
	}

	kubeInformerFactory, err := utils.GetInformerFactoryByExecutor(gpm.kubernetesClient, fv1.ExecutorTypePoolmgr)
	if err != nil {
		return nil, err
	}
	gpm.podInformer = kubeInformerFactory.Core().V1().Pods().Informer()
	return gpm, nil
}

func (gpm *GenericPoolManager) Run(ctx context.Context) {
	go gpm.service()
	gpm.poolPodC.InjectGpm(gpm)
	go gpm.podInformer.Run(ctx.Done())
	go gpm.WebsocketStartEventChecker(gpm.kubernetesClient)
	go gpm.NoActiveConnectionEventChecker(gpm.kubernetesClient)
	go gpm.idleObjectReaper()
	go gpm.poolPodC.Run(ctx.Done())
}

func (gpm *GenericPoolManager) GetTypeName(ctx context.Context) fv1.ExecutorType {
	return fv1.ExecutorTypePoolmgr
}

func (gpm *GenericPoolManager) GetFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	// from Func -> get Env
	gpm.logger.Debug("getting environment for function", zap.String("function", fn.ObjectMeta.Name))
	env, err := gpm.getFunctionEnv(ctx, fn)
	if err != nil {
		return nil, err
	}

	pool, created, err := gpm.getPool(ctx, env)
	if err != nil {
		return nil, err
	}

	if created {
		gpm.logger.Info("created pool for the environment", zap.String("env", env.ObjectMeta.Name), zap.String("namespace", gpm.namespace))
	}

	// from GenericPool -> get one function container
	// (this also adds to the cache)
	gpm.logger.Debug("getting function service from pool", zap.String("function", fn.ObjectMeta.Name))
	return pool.getFuncSvc(ctx, fn)
}

func (gpm *GenericPoolManager) GetFuncSvcFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	return nil, nil
}

func (gpm *GenericPoolManager) GetFuncSvcFromPoolCache(ctx context.Context, fn *fv1.Function, requestsPerPod int) (*fscache.FuncSvc, int, error) {
	return gpm.fsCache.GetFuncSvc(&fn.ObjectMeta, requestsPerPod)
}

func (gpm *GenericPoolManager) DeleteFuncSvcFromCache(ctx context.Context, fsvc *fscache.FuncSvc) {
	gpm.fsCache.DeleteFunctionSvc(fsvc)
}

func (gpm *GenericPoolManager) UnTapService(ctx context.Context, key string, svcHost string) {
	gpm.fsCache.MarkAvailable(key, svcHost)
}

func (gpm *GenericPoolManager) TapService(ctx context.Context, svcHost string) error {
	err := gpm.fsCache.TouchByAddress(svcHost)
	if err != nil {
		return err
	}
	return nil
}

func (gpm *GenericPoolManager) getPodInfo(ctx context.Context, obj apiv1.ObjectReference) (*apiv1.Pod, error) {
	store := gpm.podInformer.GetStore()

	item, exists, err := store.Get(obj)
	if err != nil || !exists {
		item, exists, err = store.GetByKey(fmt.Sprintf("%s/%s", obj.Namespace, obj.Name))
	}

	if err != nil || !exists {
		gpm.logger.Debug("Falling back to getting pod info from k8s API -- this may cause performance issues for your function.")
		pod, err := gpm.kubernetesClient.CoreV1().Pods(obj.Namespace).Get(ctx, obj.Name, metav1.GetOptions{})
		return pod, err
	}

	pod := item.(*apiv1.Pod)
	return pod, nil
}

// IsValid checks if pod is not deleted and that it has the address passed as the argument. Also checks that all the
// containers in it are reporting a ready status for the healthCheck.
func (gpm *GenericPoolManager) IsValid(ctx context.Context, fsvc *fscache.FuncSvc) bool {
	for _, obj := range fsvc.KubernetesObjects {
		if strings.ToLower(obj.Kind) == "pod" {
			pod, err := gpm.getPodInfo(ctx, obj)
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
		gpm.logger.Info("created pool for the environment", zap.String("env", env.ObjectMeta.Name), zap.String("namespace", gpm.namespace))
	}

	funcSvc, err := gp.fsCache.GetByFunction(&f.ObjectMeta)
	if err != nil {
		return err
	}

	gp.fsCache.DeleteEntry(funcSvc)

	funcLabels := gp.labelsForFunction(&f.ObjectMeta)

	podList, err := gpm.kubernetesClient.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
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
	envs, err := gpm.fissionClient.CoreV1().Environments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		gpm.logger.Error("error getting environment list", zap.Error(err))
		return
	}

	envMap := make(map[string]fv1.Environment, len(envs.Items))
	wg := &sync.WaitGroup{}

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
					gpm.logger.Info("created pool for the environment", zap.String("env", env.ObjectMeta.Name), zap.String("namespace", gpm.namespace))
				}
			}()
		}

		// create environment map for later use
		key := fmt.Sprintf("%v/%v", env.ObjectMeta.Namespace, env.ObjectMeta.Name)
		envMap[key] = env
	}

	l := map[string]string{
		fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypePoolmgr),
	}

	podList, err := gpm.kubernetesClient.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
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
				ns := gpm.namespace
				if req.env.ObjectMeta.Namespace != metav1.NamespaceDefault {
					ns = req.env.ObjectMeta.Namespace
				}
				pool = MakeGenericPool(gpm.logger, gpm.fissionClient, gpm.kubernetesClient,
					gpm.metricsClient, req.env, ns, gpm.namespace, gpm.fsCache,
					gpm.fetcherConfig, gpm.instanceID, gpm.enableIstio)
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
	gpm.requestChannel <- &request{
		requestType: CLEANUP_POOL,
		env:         env,
	}
}

func (gpm *GenericPoolManager) getFunctionEnv(ctx context.Context, fn *fv1.Function) (*fv1.Environment, error) {
	var env *fv1.Environment

	// Cached ?
	// TODO: the cache should be able to search by <env name, fn namespace> instead of function metadata.
	result, err := gpm.functionEnv.Get(crd.CacheKey(&fn.ObjectMeta))
	if err == nil {
		env = result.(*fv1.Environment)
		return env, nil
	}

	// Get env from controller
	env, err = gpm.fissionClient.CoreV1().Environments(fn.Spec.Environment.Namespace).Get(ctx, fn.Spec.Environment.Name, metav1.GetOptions{})
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
func (gpm *GenericPoolManager) idleObjectReaper() {
	ctx := context.Background()
	pollSleep := 5 * time.Second

	for {
		time.Sleep(pollSleep)

		envs, err := gpm.fissionClient.CoreV1().Environments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			gpm.logger.Error("failed to get environment list", zap.Error(err))
			continue
		}

		envList := make(map[k8sTypes.UID]struct{})
		for _, env := range envs.Items {
			envList[env.ObjectMeta.UID] = struct{}{}
		}

		fns, err := gpm.fissionClient.CoreV1().Functions(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			gpm.logger.Error("failed to get environment list", zap.Error(err))
			continue
		}

		fnList := make(map[k8sTypes.UID]fv1.Function)
		for i, fn := range fns.Items {
			fnList[fn.ObjectMeta.UID] = fns.Items[i]
		}

		funcSvcs, err := gpm.fsCache.ListOldForPool(pollSleep)
		if err != nil {
			gpm.logger.Error("error reaping idle pods", zap.Error(err))
			continue
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
			idleTime := (time.Since(fsvc.Atime) - idlePodReapTime).Seconds()
			gpm.fsCache.IdleTime(fsvc.Name, fsvc.Address, idleTime)

			go func() {
				startTime := time.Now()
				deleted, err := gpm.fsCache.DeleteOldPoolCache(fsvc, idlePodReapTime)
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
						gpm.fsCache.ReapTime(fsvc.Function.Name, fsvc.Address, time.Since(startTime).Seconds())
					}
				}
			}()
		}
	}
}

// WebsocketStartEventChecker checks if the pod has emitted a websocket connection start event
func (gpm *GenericPoolManager) WebsocketStartEventChecker(kubeClient *kubernetes.Clientset) {

	informer := k8sCache.NewSharedInformer(
		&k8sCache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.FieldSelector = "involvedObject.kind=Pod,type=Normal,reason=WsConnectionStarted"
				return kubeClient.CoreV1().Events(apiv1.NamespaceAll).List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.FieldSelector = "involvedObject.kind=Pod,type=Normal,reason=WsConnectionStarted"
				return kubeClient.CoreV1().Events(apiv1.NamespaceAll).Watch(context.TODO(), options)
			},
		},
		&apiv1.Event{},
		0,
	)

	stopper := make(chan struct{})
	defer close(stopper)
	informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mObj := obj.(metav1.Object)
			gpm.logger.Info("Websocket event detected for pod",
				zap.String("Pod name", mObj.GetName()))

			podName := strings.SplitAfter(mObj.GetName(), ".")
			if fsvc, ok := gpm.fsCache.PodToFsvc.Load(strings.TrimSuffix(podName[0], ".")); ok {
				fsvc, ok := fsvc.(*fscache.FuncSvc)
				if !ok {
					gpm.logger.Error("could not covert item from PodToFsvc")
					return
				}
				gpm.fsCache.WebsocketFsvc.Store(fsvc.Name, true)
			}
		},
	})
	informer.Run(stopper)

}

// NoActiveConnectionEventChecker checks if the pod has emitted an inactive event
func (gpm *GenericPoolManager) NoActiveConnectionEventChecker(kubeClient *kubernetes.Clientset) {

	informer := k8sCache.NewSharedInformer(
		&k8sCache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.FieldSelector = "involvedObject.kind=Pod,type=Normal,reason=NoActiveConnections"
				return kubeClient.CoreV1().Events(apiv1.NamespaceAll).List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.FieldSelector = "involvedObject.kind=Pod,type=Normal,reason=NoActiveConnections"
				return kubeClient.CoreV1().Events(apiv1.NamespaceAll).Watch(context.TODO(), options)
			},
		},
		&apiv1.Event{},
		0,
	)

	stopper := make(chan struct{})
	defer close(stopper)
	informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mObj := obj.(metav1.Object)
			gpm.logger.Info("Inactive event detected for pod",
				zap.String("Pod name", mObj.GetName()))

			podName := strings.SplitAfter(mObj.GetName(), ".")
			if fsvc, ok := gpm.fsCache.PodToFsvc.Load(strings.TrimSuffix(podName[0], ".")); ok {
				fsvc, ok := fsvc.(*fscache.FuncSvc)
				if !ok {
					gpm.logger.Error("could not covert value from PodToFsvc")
					return
				}
				gpm.fsCache.DeleteFunctionSvc(fsvc)
				for i := range fsvc.KubernetesObjects {
					gpm.logger.Info("release idle function resources due to  inactivity",
						zap.String("function", fsvc.Function.Name),
						zap.String("address", fsvc.Address),
						zap.String("executor", string(fsvc.Executor)),
						zap.String("pod", fsvc.Name),
					)
					ctx := context.Background()
					reaper.CleanupKubeObject(ctx, gpm.logger, gpm.kubernetesClient, &fsvc.KubernetesObjects[i])
					time.Sleep(50 * time.Millisecond)
				}
			}

		},
	})
	informer.Run(stopper)

}
