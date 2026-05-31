// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
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
	"golang.org/x/sync/errgroup"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/utils"
)

// PoolPodController watches ReplicaSets and reaps a pool's specialized pods when
// the pool scales to zero (a ReplicaSet with zero replicas) or its Environment is
// deleted. The Function and Environment watches it used to host are now
// controller-runtime reconcilers on the executor Manager (see reconciler.go);
// this controller keeps only the k8s-native pod machinery, which is tightly
// coupled to the gpm actor and migrated separately.
type (
	PoolPodController struct {
		logger           logr.Logger
		kubernetesClient kubernetes.Interface
		nsResolver       *utils.NamespaceResolver

		// podLister can list/get pods from the shared informer's store
		podLister map[string]corelisters.PodLister

		// podListerSynced returns true if the pod store has been synced at least once.
		podListerSynced map[string]k8sCache.InformerSynced

		spCleanupPodQueue workqueue.TypedRateLimitingInterface[string]

		gpm *GenericPoolManager
	}
)

func NewPoolPodController(ctx context.Context, logger logr.Logger,
	kubernetesClient kubernetes.Interface,
	gpmInformerFactory map[string]k8sInformers.SharedInformerFactory) (*PoolPodController, error) {
	logger = logger.WithName("pool_pod_controller")
	p := &PoolPodController{
		logger:            logger,
		nsResolver:        utils.DefaultNSResolver(),
		kubernetesClient:  kubernetesClient,
		podLister:         make(map[string]corelisters.PodLister),
		podListerSynced:   make(map[string]k8sCache.InformerSynced),
		spCleanupPodQueue: workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[string](), workqueue.TypedRateLimitingQueueConfig[string]{Name: "SpecializedPodCleanupQueue"}),
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

// cleanupSpecializedPodsForEnv enqueues an environment's specialized pods for
// cleanup. Called by the Environment reconciler on delete (via the gpm actor's
// cleanupEnvPool), after the warm pool itself has been destroyed.
func (p *PoolPodController) cleanupSpecializedPodsForEnv(ctx context.Context, env *fv1.Environment) {
	specializePodLabels := getSpecializedPodLabels(env)
	ns := p.nsResolver.ResolveNamespace(p.nsResolver.FunctionNamespace)
	podLister, ok := p.podLister[ns]
	if !ok {
		p.logger.Error(nil, "no pod lister found for namespace", "namespace", ns)
		return
	}
	specializedPods, err := podLister.Pods(ns).List(labels.SelectorFromSet(specializePodLabels))
	if err != nil {
		p.logger.Error(err, "failed to list specialized pods")
		return
	}
	if len(specializedPods) == 0 {
		return
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
}

func (p *PoolPodController) Run(ctx context.Context, stopCh <-chan struct{}, mgr *errgroup.Group) {
	defer utilruntime.HandleCrash()
	defer p.spCleanupPodQueue.ShutDown()
	// Wait for the caches to be synced before starting workers
	p.logger.Info("Waiting for informer caches to sync")

	waitSynced := make([]k8sCache.InformerSynced, 0)
	for _, synced := range p.podListerSynced {
		waitSynced = append(waitSynced, synced)
	}
	if ok := k8sCache.WaitForCacheSync(stopCh, waitSynced...); !ok {
		// Usually means the context was cancelled (shutdown or loss of
		// leadership). Stop cleanly instead of taking the whole process down.
		p.logger.Info("failed to wait for caches to sync; stopping pool pod controller")
		return
	}
	mgr.Go(func() error {
		wait.Until(p.workerRun(ctx, "spCleanupPodQueue", p.spCleanupPodQueueProcessFunc), time.Second, stopCh)
		return nil
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
