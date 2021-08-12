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
	"strings"
	"time"

	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	finformerv1 "github.com/fission/fission/pkg/generated/informers/externalversions/core/v1"
	flisterv1 "github.com/fission/fission/pkg/generated/listers/core/v1"
)

type (
	PoolPodController struct {
		logger           *zap.Logger
		kubernetesClient *kubernetes.Clientset
		namespace        string
		enableIstio      bool

		envLister       flisterv1.EnvironmentLister
		envListerSynced cache.InformerSynced

		envCreateUpdateQueue workqueue.RateLimitingInterface
		envDeleteQueue       workqueue.RateLimitingInterface

		gpm *GenericPoolManager
	}
)

func NewPoolPodController(logger *zap.Logger,
	kubernetesClient *kubernetes.Clientset,
	namespace string,
	enableIstio bool,
	funcInformer finformerv1.FunctionInformer,
	pkgInformer finformerv1.PackageInformer,
	envInformer finformerv1.EnvironmentInformer) *PoolPodController {
	p := &PoolPodController{
		logger:           logger,
		kubernetesClient: kubernetesClient,
		namespace:        namespace,
		enableIstio:      enableIstio,

		envCreateUpdateQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "EnvAddUpdateQueue"),
		envDeleteQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "EnvDeleteQueue"),
	}
	funcInformer.Informer().AddEventHandler(FunctionEventHandlers(p.logger, p.kubernetesClient, p.namespace, p.enableIstio))
	pkgInformer.Informer().AddEventHandler(PackageEventHandlers(p.logger, p.kubernetesClient, p.namespace))
	envInformer.Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
		AddFunc:    p.enqueueEnvAdd,
		UpdateFunc: p.enqueueEnvUpdate,
		DeleteFunc: p.enqueueEnvDelete,
	})
	p.envLister = envInformer.Lister()
	p.envListerSynced = envInformer.Informer().HasSynced
	p.logger.Info("pool pod controller handlers registered")
	return p
}

func (p *PoolPodController) InjectGpm(gpm *GenericPoolManager) {
	p.gpm = gpm
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

func (p *PoolPodController) Run(stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer p.envCreateUpdateQueue.ShutDown()
	defer p.envDeleteQueue.ShutDown()

	// Wait for the caches to be synced before starting workers
	p.logger.Info("Waiting for informer caches to sync")
	if ok := k8sCache.WaitForCacheSync(stopCh, p.envListerSynced); !ok {
		p.logger.Fatal("failed to wait for caches to sync")
	}
	for i := 0; i < 4; i++ {
		go wait.Until(p.runEnvCreateUpdateWorker, time.Second, stopCh)
	}
	go wait.Until(p.runEnvDeleteWorker, time.Second, stopCh)
	p.logger.Info("Started workers for poolPodController")
	<-stopCh
	p.logger.Info("Shutting down workers for poolPodController")
}

func (p *PoolPodController) runEnvCreateUpdateWorker() {
	p.logger.Debug("Starting runEnvCreateUpdateWorker worker")
	maxRetries := 3
	handleEnv := func(env *fv1.Environment) error {
		log := p.logger.With(zap.String("env", env.ObjectMeta.Name), zap.String("namespace", env.ObjectMeta.Namespace))
		log.Debug("env reconsile request processing")
		pool, created, err := p.gpm.getPool(env)
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
			p.gpm.cleanupPool(env)
			return nil
		}
		err = pool.updatePoolDeployment(context.Background(), env)
		if err != nil {
			log.Error("error updating pool", zap.Error(err))
			return err
		}
		p.deleteSpecializedPods(env)
		return nil
	}
	processItem := func() bool {
		obj, quit := p.envCreateUpdateQueue.Get()
		if quit {
			return true
		}
		key := obj.(string)
		defer p.envCreateUpdateQueue.Done(key)

		namespace, name, err := k8sCache.SplitMetaNamespaceKey(key)
		if err != nil {
			p.logger.Error("error splitting key", zap.Error(err))
			return false
		}
		env, err := p.envLister.Environments(namespace).Get(name)
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

		err = handleEnv(env)
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
	for {
		if quit := processItem(); quit {
			p.logger.Info("Shutting down create/update worker")
			return
		}
	}
}

func (p *PoolPodController) runEnvDeleteWorker() {
	p.logger.Debug("Starting runEnvDeleteWorker worker")
	processItem := func() bool {
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
		p.gpm.cleanupPool(env)
		p.deleteSpecializedPods(env)
		p.envDeleteQueue.Forget(obj)
		return false
	}
	for {
		if quit := processItem(); quit {
			p.logger.Info("Shutting down delete worker")
			return
		}
	}
}

func (p *PoolPodController) deleteSpecializedPods(env *fv1.Environment) {
	log := p.logger.With(zap.String("env", env.ObjectMeta.Name), zap.String("namespace", env.ObjectMeta.Namespace))
	log.Debug("environment delete")
	selectorLabels := getSpecializedPodLabels(env)
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(selectorLabels).String(),
	}
	ctx := context.Background()
	podList, err := p.kubernetesClient.CoreV1().Pods(p.namespace).List(ctx, listOptions)
	if err != nil {
		log.Error("failed to list pods", zap.Error(err))
		return
	}
	if len(podList.Items) == 0 {
		log.Debug("no pods identified for cleanup after env delete")
		return
	}
	log.Info("specialized pods identified for cleanup after env delete", zap.Int("numPods", len(podList.Items)))
	for _, pod := range podList.Items {
		podName := strings.SplitAfter(pod.GetName(), ".")
		if fsvc, ok := p.gpm.fsCache.PodToFsvc.Load(strings.TrimSuffix(podName[0], ".")); ok {
			fsvc, ok := fsvc.(*fscache.FuncSvc)
			if !ok {
				log.Error("could not covert item from PodToFsvc")
			}
			p.gpm.fsCache.DeleteFunctionSvc(fsvc)
			p.gpm.fsCache.DeleteEntry(fsvc)
		}
		err = p.kubernetesClient.CoreV1().Pods(p.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		if err != nil {
			log.Error("failed to delete pod", zap.Error(err))
			continue
		}
		log.Info("cleaned specialized pod as environment deleted",
			zap.String("pod", pod.ObjectMeta.Name), zap.String("pod_namespace", pod.ObjectMeta.Namespace),
			zap.String("address", pod.Status.PodIP))
	}
}
