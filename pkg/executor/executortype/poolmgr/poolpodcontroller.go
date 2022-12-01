/*
Copyright 2021 The Fission Authors.

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
	"strings"
	"time"

	"go.uber.org/zap"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	finformerv1 "github.com/fission/fission/pkg/generated/informers/externalversions/core/v1"
	flisterv1 "github.com/fission/fission/pkg/generated/listers/core/v1"
	"github.com/fission/fission/pkg/utils"
)

type (
	PoolPodController struct {
		logger           *zap.Logger
		kubernetesClient kubernetes.Interface
		enableIstio      bool
		nsResolver       *utils.NamespaceResolver

		envLister       map[string]flisterv1.EnvironmentLister
		envListerSynced map[string]k8sCache.InformerSynced

		// podLister can list/get pods from the shared informer's store
		podLister corelisters.PodLister

		// podListerSynced returns true if the pod store has been synced at least once.
		podListerSynced k8sCache.InformerSynced

		envCreateUpdateQueue workqueue.RateLimitingInterface
		envDeleteQueue       workqueue.RateLimitingInterface

		spCleanupPodQueue workqueue.RateLimitingInterface

		gpm *GenericPoolManager
	}
)

func NewPoolPodController(ctx context.Context, logger *zap.Logger,
	kubernetesClient kubernetes.Interface,
	enableIstio bool,
	funcInformer map[string]finformerv1.FunctionInformer,
	pkgInformer map[string]finformerv1.PackageInformer,
	envInformer map[string]finformerv1.EnvironmentInformer,
	rsInformer appsinformers.ReplicaSetInformer,
	podInformer coreinformers.PodInformer) *PoolPodController {
	logger = logger.Named("pool_pod_controller")
	p := &PoolPodController{
		logger:               logger,
		nsResolver:           utils.DefaultNSResolver(),
		kubernetesClient:     kubernetesClient,
		enableIstio:          enableIstio,
		envLister:            make(map[string]flisterv1.EnvironmentLister, 0),
		envListerSynced:      make(map[string]k8sCache.InformerSynced, 0),
		envCreateUpdateQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "EnvAddUpdateQueue"),
		envDeleteQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "EnvDeleteQueue"),
		spCleanupPodQueue:    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "SpecializedPodCleanupQueue"),
	}
	for _, informer := range funcInformer {
		informer.Informer().AddEventHandler(FunctionEventHandlers(ctx, p.logger, p.kubernetesClient, p.nsResolver.ResolveNamespace(p.nsResolver.FunctionNamespace), p.enableIstio))
	}
	for _, informer := range pkgInformer {
		informer.Informer().AddEventHandler(PackageEventHandlers(ctx, p.logger, p.kubernetesClient, p.nsResolver.ResolveNamespace(p.nsResolver.FunctionNamespace)))
	}
	for ns, informer := range envInformer {
		informer.Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc:    p.enqueueEnvAdd,
			UpdateFunc: p.enqueueEnvUpdate,
			DeleteFunc: p.enqueueEnvDelete,
		})
		p.envLister[ns] = informer.Lister()
		p.envListerSynced[ns] = informer.Informer().HasSynced
	}
	rsInformer.Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
		AddFunc:    p.handleRSAdd,
		UpdateFunc: p.handleRSUpdate,
		DeleteFunc: p.handleRSDelete,
	})

	p.podLister = podInformer.Lister()
	p.podListerSynced = podInformer.Informer().HasSynced
	p.logger.Info("pool pod controller handlers registered")
	return p
}

func (p *PoolPodController) InjectGpm(gpm *GenericPoolManager) {
	p.gpm = gpm
}

func IsPodActive(p *v1.Pod) bool {
	return v1.PodSucceeded != p.Status.Phase &&
		v1.PodFailed != p.Status.Phase &&
		p.DeletionTimestamp == nil
}

func (p *PoolPodController) processRS(rs *apps.ReplicaSet) {
	if *(rs.Spec.Replicas) != 0 {
		return
	}
	logger := p.logger.With(zap.String("rs", rs.Name), zap.String("namespace", rs.Namespace))
	logger.Debug("replica set has zero replica count")
	// List all specialized pods and schedule for cleanup
	rsLabelMap, err := metav1.LabelSelectorAsMap(rs.Spec.Selector)
	if err != nil {
		p.logger.Error("Failed to parse label selector", zap.Error(err))
		return
	}
	rsLabelMap["managed"] = "false"
	specializedPods, err := p.podLister.Pods(rs.Namespace).List(labels.SelectorFromSet(rsLabelMap))
	if err != nil {
		logger.Error("Failed to list specialized pods", zap.Error(err))
	}
	if len(specializedPods) == 0 {
		return
	}
	logger.Info("specialized pods identified for cleanup with RS", zap.Int("numPods", len(specializedPods)))
	for _, pod := range specializedPods {
		if !IsPodActive(pod) {
			continue
		}
		key, err := k8sCache.MetaNamespaceKeyFunc(pod)
		if err != nil {
			logger.Error("Failed to get key for pod", zap.Error(err))
			continue
		}
		p.spCleanupPodQueue.Add(key)
	}
}

func (p *PoolPodController) handleRSAdd(obj interface{}) {
	rs, ok := obj.(*apps.ReplicaSet)
	if !ok {
		p.logger.Error("unexpected type when adding rs to pool pod controller", zap.Any("obj", obj))
		return
	}
	p.processRS(rs)
}

func (p *PoolPodController) handleRSUpdate(oldObj interface{}, newObj interface{}) {
	rs, ok := newObj.(*apps.ReplicaSet)
	if !ok {
		p.logger.Error("unexpected type when updating rs to pool pod controller", zap.Any("obj", newObj))
		return
	}
	p.processRS(rs)
}

func (p *PoolPodController) handleRSDelete(obj interface{}) {
	rs, ok := obj.(*apps.ReplicaSet)
	if !ok {
		tombstone, ok := obj.(k8sCache.DeletedFinalStateUnknown)
		if !ok {
			p.logger.Error("couldn't get object from tombstone", zap.Any("obj", obj))
			return
		}
		rs, ok = tombstone.Obj.(*apps.ReplicaSet)
		if !ok {
			p.logger.Error("tombstone contained object that is not a replicaset", zap.Any("obj", obj))
			return
		}
	}
	p.processRS(rs)
}

func (p *PoolPodController) enqueueEnvAdd(obj interface{}) {
	key, err := k8sCache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		p.logger.Error("error retrieving key from object in poolPodController", zap.Any("obj", obj))
		return
	}
	p.logger.Debug("enqueue env add", zap.String("key", key))
	p.envCreateUpdateQueue.Add(key)
}

func (p *PoolPodController) enqueueEnvUpdate(oldObj, newObj interface{}) {
	key, err := k8sCache.MetaNamespaceKeyFunc(newObj)
	if err != nil {
		p.logger.Error("error retrieving key from object in poolPodController", zap.Any("obj", key))
		return
	}
	p.logger.Debug("enqueue env update", zap.String("key", key))
	p.envCreateUpdateQueue.Add(key)
}

func (p *PoolPodController) enqueueEnvDelete(obj interface{}) {
	env, ok := obj.(*fv1.Environment)
	if !ok {
		p.logger.Error("unexpected type when deleting env to pool pod controller", zap.Any("obj", obj))
		return
	}
	p.logger.Debug("enqueue env delete", zap.Any("env", env))
	p.envDeleteQueue.Add(env)
}

func (p *PoolPodController) Run(ctx context.Context) {
	defer utilruntime.HandleCrash()
	defer p.envCreateUpdateQueue.ShutDown()
	defer p.envDeleteQueue.ShutDown()
	defer p.spCleanupPodQueue.ShutDown()
	// Wait for the caches to be synced before starting workers
	p.logger.Info("Waiting for informer caches to sync")

	waitSynced := make([]k8sCache.InformerSynced, 0)
	waitSynced = append(waitSynced, p.podListerSynced)
	for _, synced := range p.envListerSynced {
		waitSynced = append(waitSynced, synced)
	}
	if ok := k8sCache.WaitForCacheSync(ctx.Done(), waitSynced...); !ok {
		p.logger.Fatal("failed to wait for caches to sync")
	}
	for i := 0; i < 4; i++ {
		go wait.Until(p.workerRun(ctx, "envCreateUpdate", p.envCreateUpdateQueueProcessFunc), time.Second, ctx.Done())
	}
	go wait.Until(p.workerRun(ctx, "envDeleteQueue", p.envDeleteQueueProcessFunc), time.Second, ctx.Done())
	go wait.Until(p.workerRun(ctx, "spCleanupPodQueue", p.spCleanupPodQueueProcessFunc), time.Second, ctx.Done())
	p.logger.Info("Started workers for poolPodController")
	<-ctx.Done()
	p.logger.Info("Shutting down workers for poolPodController")
}

func (p *PoolPodController) workerRun(ctx context.Context, name string, processFunc func(ctx context.Context) bool) func() {
	return func() {
		p.logger.Debug("Starting worker with func", zap.String("name", name))
		for {
			if quit := processFunc(ctx); quit {
				p.logger.Info("Shutting down worker", zap.String("name", name))
				return
			}
		}
	}
}

func (p *PoolPodController) getEnvLister(namespace string) (flisterv1.EnvironmentLister, error) {
	lister, ok := p.envLister[namespace]
	if ok {
		return lister, nil
	}
	for ns, lister := range p.envLister {
		if ns == namespace {
			return lister, nil
		}
	}
	p.logger.Error("no environment lister found for namespace", zap.String("namespace", namespace))
	return nil, fmt.Errorf("no environment lister found for namespace %s", namespace)
}

func (p *PoolPodController) envCreateUpdateQueueProcessFunc(ctx context.Context) bool {
	maxRetries := 3
	handleEnv := func(ctx context.Context, env *fv1.Environment) error {
		log := p.logger.With(zap.String("env", env.ObjectMeta.Name), zap.String("namespace", env.ObjectMeta.Namespace))
		log.Debug("env reconsile request processing")
		pool, created, err := p.gpm.getPool(ctx, env)
		if err != nil {
			log.Error("error getting pool", zap.Error(err))
			return err
		}
		if created {
			log.Info("created pool for the environment")
			return nil
		}
		poolsize := getEnvPoolSize(env)
		if poolsize == 0 {
			log.Info("pool size is zero")
			p.gpm.cleanupPool(ctx, env)
			return nil
		}
		err = pool.updatePoolDeployment(ctx, env)
		if err != nil {
			log.Error("error updating pool", zap.Error(err))
			return err
		}
		// If any specialized pods are running, those would be
		// deleted by replicaSet controller.
		return nil
	}

	obj, quit := p.envCreateUpdateQueue.Get()
	if quit {
		return true
	}
	key := obj.(string)
	defer p.envCreateUpdateQueue.Done(key)

	namespace, name, err := k8sCache.SplitMetaNamespaceKey(key)
	if err != nil {
		p.logger.Error("error splitting key", zap.Error(err))
		p.envCreateUpdateQueue.Forget(key)
		return false
	}
	envLister, err := p.getEnvLister(namespace)
	if err != nil {
		p.logger.Error("error getting environment lister", zap.Error(err))
		p.envCreateUpdateQueue.Forget(key)
		return false
	}
	env, err := envLister.Environments(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		p.logger.Info("env not found", zap.String("key", key))
		p.envCreateUpdateQueue.Forget(key)
		return false
	}

	if err != nil {
		if p.envCreateUpdateQueue.NumRequeues(key) < maxRetries {
			p.envCreateUpdateQueue.AddRateLimited(key)
			p.logger.Error("error getting env, retrying", zap.Error(err))
		} else {
			p.envCreateUpdateQueue.Forget(key)
			p.logger.Error("error getting env, retrying, max retries reached", zap.Error(err))
		}
		return false
	}

	err = handleEnv(ctx, env)
	if err != nil {
		if p.envCreateUpdateQueue.NumRequeues(key) < maxRetries {
			p.envCreateUpdateQueue.AddRateLimited(key)
			p.logger.Error("error handling env from envInformer, retrying", zap.String("key", key), zap.Error(err))
		} else {
			p.envCreateUpdateQueue.Forget(key)
			p.logger.Error("error handling env from envInformer, max retries reached", zap.String("key", key), zap.Error(err))
		}
		return false
	}
	p.envCreateUpdateQueue.Forget(key)
	return false
}

func (p *PoolPodController) envDeleteQueueProcessFunc(ctx context.Context) bool {
	obj, quit := p.envDeleteQueue.Get()
	if quit {
		return true
	}
	defer p.envDeleteQueue.Done(obj)
	env, ok := obj.(*fv1.Environment)
	if !ok {
		p.logger.Error("unexpected type when deleting env to pool pod controller", zap.Any("obj", obj))
		p.envDeleteQueue.Forget(obj)
		return false
	}
	p.logger.Debug("env delete request processing")
	p.gpm.cleanupPool(ctx, env)
	specializePodLables := getSpecializedPodLabels(env)
	specializedPods, err := p.podLister.Pods(p.nsResolver.ResolveNamespace(p.nsResolver.FunctionNamespace)).List(labels.SelectorFromSet(specializePodLables))
	if err != nil {
		p.logger.Error("failed to list specialized pods", zap.Error(err))
		p.envDeleteQueue.Forget(obj)
		return false
	}
	if len(specializedPods) == 0 {
		p.envDeleteQueue.Forget(obj)
		return false
	}
	p.logger.Info("specialized pods identified for cleanup after env delete", zap.String("env", env.ObjectMeta.Name), zap.String("namespace", env.ObjectMeta.Namespace), zap.Int("count", len(specializedPods)))
	for _, pod := range specializedPods {
		if !IsPodActive(pod) {
			continue
		}
		key, err := k8sCache.MetaNamespaceKeyFunc(pod)
		if err != nil {
			p.logger.Error("Failed to get key for pod", zap.Error(err))
			continue
		}
		p.spCleanupPodQueue.Add(key)
	}
	p.envDeleteQueue.Forget(obj)
	return false
}

func (p *PoolPodController) spCleanupPodQueueProcessFunc(ctx context.Context) bool {
	maxRetries := 3
	obj, quit := p.spCleanupPodQueue.Get()
	if quit {
		return true
	}
	key := obj.(string)
	defer p.spCleanupPodQueue.Done(key)
	namespace, name, err := k8sCache.SplitMetaNamespaceKey(key)
	if err != nil {
		p.logger.Error("error splitting key", zap.Error(err))
		p.spCleanupPodQueue.Forget(key)
		return false
	}
	pod, err := p.podLister.Pods(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		p.logger.Info("pod not found", zap.String("key", key))
		p.spCleanupPodQueue.Forget(key)
		return false
	}
	if !IsPodActive(pod) {
		p.logger.Info("pod not active", zap.String("key", key))
		p.spCleanupPodQueue.Forget(key)
		return false
	}
	if err != nil {
		if p.spCleanupPodQueue.NumRequeues(key) < maxRetries {
			p.spCleanupPodQueue.AddRateLimited(key)
			p.logger.Error("error getting pod, retrying", zap.Error(err))
		} else {
			p.spCleanupPodQueue.Forget(key)
			p.logger.Error("error getting pod, max retries reached", zap.Error(err))
		}
		return false
	}
	podName := strings.SplitAfter(pod.GetName(), ".")
	if fsvc, ok := p.gpm.fsCache.PodToFsvc.Load(strings.TrimSuffix(podName[0], ".")); ok {
		fsvc, ok := fsvc.(*fscache.FuncSvc)
		if ok {
			p.gpm.fsCache.DeleteFunctionSvc(ctx, fsvc)
			p.gpm.fsCache.DeleteEntry(fsvc)
		} else {
			p.logger.Error("could not convert item from PodToFsvc", zap.String("key", key))
		}
	}
	err = p.kubernetesClient.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		p.logger.Error("failed to delete pod", zap.Error(err), zap.String("pod", name), zap.String("pod_namespace", namespace))
		return false
	}
	p.logger.Info("cleaned specialized pod as environment update/deleted",
		zap.String("pod", name), zap.String("pod_namespace", namespace),
		zap.String("address", pod.Status.PodIP))
	p.spCleanupPodQueue.Forget(key)
	return false
}
