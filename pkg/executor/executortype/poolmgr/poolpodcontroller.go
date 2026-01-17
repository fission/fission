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
	"os"
	"strings"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sInformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/fscache"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	flisterv1 "github.com/fission/fission/pkg/generated/listers/core/v1"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

type (
	PoolPodController struct {
		logger           logr.Logger
		kubernetesClient kubernetes.Interface
		enableIstio      bool
		nsResolver       *utils.NamespaceResolver

		envLister       map[string]flisterv1.EnvironmentLister
		envListerSynced map[string]k8sCache.InformerSynced

		// podLister can list/get pods from the shared informer's store
		podLister map[string]corelisters.PodLister

		// podListerSynced returns true if the pod store has been synced at least once.
		podListerSynced map[string]k8sCache.InformerSynced

		envCreateUpdateQueue workqueue.TypedRateLimitingInterface[string]
		envDeleteQueue       workqueue.TypedRateLimitingInterface[*fv1.Environment]

		spCleanupPodQueue workqueue.TypedRateLimitingInterface[string]

		gpm *GenericPoolManager
	}
)

func NewPoolPodController(ctx context.Context, logger logr.Logger,
	kubernetesClient kubernetes.Interface,
	enableIstio bool,
	finformerFactory map[string]genInformer.SharedInformerFactory,
	gpmInformerFactory map[string]k8sInformers.SharedInformerFactory) (*PoolPodController, error) {
	logger = logger.WithName("pool_pod_controller")
	p := &PoolPodController{
		logger:               logger,
		nsResolver:           utils.DefaultNSResolver(),
		kubernetesClient:     kubernetesClient,
		enableIstio:          enableIstio,
		envLister:            make(map[string]flisterv1.EnvironmentLister, 0),
		envListerSynced:      make(map[string]k8sCache.InformerSynced, 0),
		podLister:            make(map[string]corelisters.PodLister),
		podListerSynced:      make(map[string]k8sCache.InformerSynced),
		envCreateUpdateQueue: workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[string](), workqueue.TypedRateLimitingQueueConfig[string]{Name: "EnvAddUpdateQueue"}),
		envDeleteQueue:       workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[*fv1.Environment](), workqueue.TypedRateLimitingQueueConfig[*fv1.Environment]{Name: "EnvDeleteQueue"}),
		spCleanupPodQueue:    workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[string](), workqueue.TypedRateLimitingQueueConfig[string]{Name: "SpecializedPodCleanupQueue"}),
	}
	if p.enableIstio {
		for _, factory := range finformerFactory {
			_, err := factory.Core().V1().Functions().Informer().AddEventHandler(FunctionEventHandlers(ctx, p.logger, p.kubernetesClient, p.nsResolver.ResolveNamespace(p.nsResolver.FunctionNamespace), p.enableIstio))
			if err != nil {
				return nil, err
			}
		}
	}
	for _, factory := range finformerFactory {
		_, err := factory.Core().V1().Functions().Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			DeleteFunc: p.handleFuncDelete,
		})
		if err != nil {
			return nil, err
		}
	}
	for ns, informer := range finformerFactory {
		_, err := informer.Core().V1().Environments().Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc:    p.enqueueEnvAdd,
			UpdateFunc: p.enqueueEnvUpdate,
			DeleteFunc: p.enqueueEnvDelete,
		})
		if err != nil {
			return nil, err
		}
		p.envLister[ns] = informer.Core().V1().Environments().Lister()
		p.envListerSynced[ns] = informer.Core().V1().Environments().Informer().HasSynced
	}
	for ns, informerFactory := range gpmInformerFactory {
		_, err := informerFactory.Apps().V1().ReplicaSets().Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc:    p.handleRSAdd,
			UpdateFunc: p.handleRSUpdate,
			DeleteFunc: p.handleRSDelete,
		})
		if err != nil {
			return nil, err
		}
		p.podListerSynced[ns] = informerFactory.Core().V1().Pods().Informer().HasSynced
		p.podLister[ns] = informerFactory.Core().V1().Pods().Lister()
	}

	p.logger.Info("pool pod controller handlers registered")
	return p, nil
}

func (p *PoolPodController) InjectGpm(gpm *GenericPoolManager) {
	p.gpm = gpm
}

func IsPodActive(p *v1.Pod) bool {
	return v1.PodSucceeded != p.Status.Phase &&
		v1.PodFailed != p.Status.Phase &&
		p.DeletionTimestamp == nil
}

func (p *PoolPodController) handleFuncDelete(obj any) {
	fn := obj.(*fv1.Function)
	p.gpm.fsCache.MarkFuncDeleted(crd.CacheKeyURGFromMeta(&fn.ObjectMeta))
}

func (p *PoolPodController) processRS(rs *apps.ReplicaSet) {
	if *(rs.Spec.Replicas) != 0 {
		return
	}
	logger := p.logger.WithValues("rs", rs.Name, "namespace", rs.Namespace)
	logger.V(1).Info("replica set has zero replica count")
	// List all specialized pods and schedule for cleanup
	rsLabelMap, err := metav1.LabelSelectorAsMap(rs.Spec.Selector)
	if err != nil {
		p.logger.Error(err, "Failed to parse label selector")
		return
	}
	rsLabelMap["managed"] = "false"
	specializedPods, err := p.podLister[rs.Namespace].Pods(rs.Namespace).List(labels.SelectorFromSet(rsLabelMap))
	if err != nil {
		logger.Error(err, "Failed to list specialized pods")
	}
	if len(specializedPods) == 0 {
		return
	}
	logger.Info("specialized pods identified for cleanup with RS", "numPods", len(specializedPods))
	for _, pod := range specializedPods {
		if !IsPodActive(pod) {
			continue
		}
		key, err := k8sCache.MetaNamespaceKeyFunc(pod)
		if err != nil {
			logger.Error(err, "Failed to get key for pod")
			continue
		}
		p.spCleanupPodQueue.Add(key)
	}
}

func (p *PoolPodController) handleRSAdd(obj any) {
	rs, ok := obj.(*apps.ReplicaSet)
	if !ok {
		p.logger.Info("unexpected type when adding rs to pool pod controller", "obj", obj)
		return
	}
	p.processRS(rs)
}

func (p *PoolPodController) handleRSUpdate(oldObj any, newObj any) {
	rs, ok := newObj.(*apps.ReplicaSet)
	if !ok {
		p.logger.Error(nil, "unexpected type when updating rs to pool pod controller", "obj", newObj)
		return
	}
	p.processRS(rs)
}

func (p *PoolPodController) handleRSDelete(obj any) {
	rs, ok := obj.(*apps.ReplicaSet)
	if !ok {
		tombstone, ok := obj.(k8sCache.DeletedFinalStateUnknown)
		if !ok {
			p.logger.Error(nil, "couldn't get object from tombstone", "obj", obj)
			return
		}
		rs, ok = tombstone.Obj.(*apps.ReplicaSet)
		if !ok {
			p.logger.Error(nil, "tombstone contained object that is not a replicaset", "obj", obj)
			return
		}
	}
	p.processRS(rs)
}

func (p *PoolPodController) enqueueEnvAdd(obj any) {
	key, err := k8sCache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		p.logger.Error(nil, "error retrieving key from object in poolPodController", "obj", obj)
		return
	}
	p.logger.V(1).Info("enqueue env add", "key", key)
	p.envCreateUpdateQueue.Add(key)
}

func (p *PoolPodController) enqueueEnvUpdate(oldObj, newObj any) {
	key, err := k8sCache.MetaNamespaceKeyFunc(newObj)
	if err != nil {
		p.logger.Error(nil, "error retrieving key from object in poolPodController", "obj", key)
		return
	}
	p.logger.V(1).Info("enqueue env update", "key", key)
	p.envCreateUpdateQueue.Add(key)
}

func (p *PoolPodController) enqueueEnvDelete(obj any) {
	env, ok := obj.(*fv1.Environment)
	if !ok {
		p.logger.Error(nil, "unexpected type when deleting env to pool pod controller", "obj", obj)
		return
	}
	p.logger.V(1).Info("enqueue env delete", "env", env)
	p.envDeleteQueue.Add(env)
}

func (p *PoolPodController) Run(ctx context.Context, stopCh <-chan struct{}, mgr manager.Interface) {
	defer utilruntime.HandleCrash()
	defer p.envCreateUpdateQueue.ShutDown()
	defer p.envDeleteQueue.ShutDown()
	defer p.spCleanupPodQueue.ShutDown()
	// Wait for the caches to be synced before starting workers
	p.logger.Info("Waiting for informer caches to sync")

	waitSynced := make([]k8sCache.InformerSynced, 0)
	for _, synced := range p.podListerSynced {
		waitSynced = append(waitSynced, synced)
	}
	for _, synced := range p.envListerSynced {
		waitSynced = append(waitSynced, synced)
	}
	if ok := k8sCache.WaitForCacheSync(stopCh, waitSynced...); !ok {
		p.logger.Info("failed to wait for caches to sync")
		os.Exit(1)
	}
	for range 4 {
		mgr.Add(ctx, func(ctx context.Context) {
			wait.Until(p.workerRun(ctx, "envCreateUpdate", p.envCreateUpdateQueueProcessFunc), time.Second, stopCh)
		})
	}
	mgr.Add(ctx, func(ctx context.Context) {
		wait.Until(p.workerRun(ctx, "envDeleteQueue", p.envDeleteQueueProcessFunc), time.Second, stopCh)
	})
	mgr.Add(ctx, func(ctx context.Context) {
		wait.Until(p.workerRun(ctx, "spCleanupPodQueue", p.spCleanupPodQueueProcessFunc), time.Second, stopCh)
	})
	p.logger.Info("Started workers for poolPodController")
	<-stopCh
	p.logger.Info("Shutting down workers for poolPodController")
}

func (p *PoolPodController) workerRun(ctx context.Context, name string, processFunc func(ctx context.Context) bool) func() {
	return func() {
		p.logger.V(1).Info("Starting worker with func", "name", name)
		for {
			if quit := processFunc(ctx); quit {
				p.logger.Info("Shutting down worker", "name", name)
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
	p.logger.Error(nil, "no environment lister found for namespace", "namespace", namespace)
	return nil, fmt.Errorf("no environment lister found for namespace %s", namespace)
}

func (p *PoolPodController) envCreateUpdateQueueProcessFunc(ctx context.Context) bool {
	maxRetries := 3
	handleEnv := func(ctx context.Context, env *fv1.Environment) error {
		log := p.logger.WithValues("env", env.Name, "namespace", env.Namespace)
		log.V(1).Info("env reconsile request processing")
		pool, created, err := p.gpm.getPool(ctx, env)
		if err != nil {
			log.Error(err, "error getting pool")
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
			log.Error(err, "error updating pool")
			return err
		}
		// If any specialized pods are running, those would be
		// deleted by replicaSet controller.
		return nil
	}

	key, quit := p.envCreateUpdateQueue.Get()
	if quit {
		return true
	}
	defer p.envCreateUpdateQueue.Done(key)

	namespace, name, err := k8sCache.SplitMetaNamespaceKey(key)
	if err != nil {
		p.logger.Error(err, "error splitting key")
		p.envCreateUpdateQueue.Forget(key)
		return false
	}
	envLister, err := p.getEnvLister(namespace)
	if err != nil {
		p.logger.Error(err, "error getting environment lister")
		p.envCreateUpdateQueue.Forget(key)
		return false
	}
	env, err := envLister.Environments(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		p.logger.Info("env not found", "key", key)
		p.envCreateUpdateQueue.Forget(key)
		return false
	}

	if err != nil {
		if p.envCreateUpdateQueue.NumRequeues(key) < maxRetries {
			p.envCreateUpdateQueue.AddRateLimited(key)
			p.logger.Error(err, "error getting env, retrying")
		} else {
			p.envCreateUpdateQueue.Forget(key)
			p.logger.Error(err, "error getting env, retrying, max retries reached")
		}
		return false
	}

	err = handleEnv(ctx, env)
	if err != nil {
		if p.envCreateUpdateQueue.NumRequeues(key) < maxRetries {
			p.envCreateUpdateQueue.AddRateLimited(key)
			p.logger.Error(err, "error handling env from envInformer, retrying", "key", key)
		} else {
			p.envCreateUpdateQueue.Forget(key)
			p.logger.Error(err, "error handling env from envInformer, max retries reached", "key", key)
		}
		return false
	}
	p.envCreateUpdateQueue.Forget(key)
	return false
}

func (p *PoolPodController) envDeleteQueueProcessFunc(ctx context.Context) bool {
	env, quit := p.envDeleteQueue.Get()
	if quit {
		return true
	}
	defer p.envDeleteQueue.Done(env)
	p.logger.V(1).Info("env delete request processing")
	p.gpm.cleanupPool(ctx, env)
	specializePodLables := getSpecializedPodLabels(env)
	ns := p.nsResolver.ResolveNamespace(p.nsResolver.FunctionNamespace)
	podLister, ok := p.podLister[ns]
	if !ok {
		p.logger.Error(nil, "no pod lister found for namespace", "namespace", ns)
		p.envDeleteQueue.Forget(env)
		return false
	}
	specializedPods, err := podLister.Pods(ns).List(labels.SelectorFromSet(specializePodLables))
	if err != nil {
		p.logger.Error(err, "failed to list specialized pods")
		p.envDeleteQueue.Forget(env)
		return false
	}
	if len(specializedPods) == 0 {
		p.envDeleteQueue.Forget(env)
		return false
	}
	p.logger.Info("specialized pods identified for cleanup after env delete", "env", env.Name, "namespace", env.Namespace, "count", len(specializedPods))
	for _, pod := range specializedPods {
		if !IsPodActive(pod) {
			continue
		}
		key, err := k8sCache.MetaNamespaceKeyFunc(pod)
		if err != nil {
			p.logger.Error(err, "Failed to get key for pod")
			continue
		}
		p.spCleanupPodQueue.Add(key)
	}
	p.envDeleteQueue.Forget(env)
	return false
}

func (p *PoolPodController) spCleanupPodQueueProcessFunc(ctx context.Context) bool {
	maxRetries := 3
	key, quit := p.spCleanupPodQueue.Get()
	if quit {
		return true
	}
	defer p.spCleanupPodQueue.Done(key)
	namespace, name, err := k8sCache.SplitMetaNamespaceKey(key)
	if err != nil {
		p.logger.Error(err, "error splitting key")
		p.spCleanupPodQueue.Forget(key)
		return false
	}
	pod, err := p.podLister[namespace].Pods(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		p.logger.Info("pod not found", "key", key)
		p.spCleanupPodQueue.Forget(key)
		return false
	}
	if !IsPodActive(pod) {
		p.logger.Info("pod not active", "key", key)
		p.spCleanupPodQueue.Forget(key)
		return false
	}
	if err != nil {
		if p.spCleanupPodQueue.NumRequeues(key) < maxRetries {
			p.spCleanupPodQueue.AddRateLimited(key)
			p.logger.Error(err, "error getting pod, retrying")
		} else {
			p.spCleanupPodQueue.Forget(key)
			p.logger.Error(err, "error getting pod, max retries reached")
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
			p.logger.Error(nil, "could not convert item from PodToFsvc", "key", key)
		}
	}
	err = p.kubernetesClient.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		p.logger.Error(err, "failed to delete pod", "pod", name, "pod_namespace", namespace)
		return false
	}
	p.logger.Info("cleaned specialized pod as environment update/deleted",
		"pod", name, "pod_namespace", namespace,
		"address", pod.Status.PodIP)
	p.spCleanupPodQueue.Forget(key)
	return false
}
