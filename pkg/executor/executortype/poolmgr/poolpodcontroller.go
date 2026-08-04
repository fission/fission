// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"fmt"
	"strings"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

		spCleanupPodQueue workqueue.TypedRateLimitingInterface[string]

		gpm *GenericPoolManager
	}
)

// NewPoolPodController returns the specialized-pod cleanup controller. Pod reads
// now go through the executor Manager cache (p.gpm.crClient), so there is no
// informer factory or pod lister to wire up here.
func NewPoolPodController(logger logr.Logger, kubernetesClient kubernetes.Interface) *PoolPodController {
	return &PoolPodController{
		logger:            logger.WithName("pool_pod_controller"),
		nsResolver:        utils.DefaultNSResolver(),
		kubernetesClient:  kubernetesClient,
		spCleanupPodQueue: workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[string](), workqueue.TypedRateLimitingQueueConfig[string]{Name: "SpecializedPodCleanupQueue"}),
	}
}

func (p *PoolPodController) InjectGpm(gpm *GenericPoolManager) {
	p.gpm = gpm
}

func IsPodActive(p *v1.Pod) bool {
	return v1.PodSucceeded != p.Status.Phase &&
		v1.PodFailed != p.Status.Phase &&
		p.DeletionTimestamp == nil
}

// processRS reaps a retired pool template's specialized pods — but ONLY when
// the retirement means their runtime is stale or their pool is gone, never
// merely because the executor restarted.
//
// A zero-replica pool RS has exactly two producers, and they demand opposite
// treatment. An ENVIRONMENT change (new runtime image) rolls the pool and the
// specialized pods born from the old template must be recycled onto the new
// runtime — before RFC-0028 this was the ONLY reading, and reconcileEnvPool
// still relies on it. An EXECUTOR-side template change (new FETCHER_IMAGE on
// upgrade, fetcher config) also rolls the pool — and here the specialized
// pods must SURVIVE: they are already specialized, their runtime is current,
// and deleting them is precisely the warm-pod kill that made every upgrade
// drop ~4% of poolmgr requests (mechanism B in the RFC-0028 gate work).
// The ENVIRONMENT_GENERATION template label is what tells the two apart.
//
// Errors propagate (-> controller-runtime backoff requeue): a transient API
// failure while deciding must retry, not silently drop a legitimate cleanup.
func (p *PoolPodController) processRS(ctx context.Context, rs *apps.ReplicaSet) error {
	if *(rs.Spec.Replicas) != 0 {
		return nil
	}
	logger := p.logger.WithValues("rs", rs.Name, "namespace", rs.Namespace)
	logger.V(1).Info("replica set has zero replica count")
	// List all specialized pods born from this template (the RS selector
	// includes pod-template-hash; managed=false overrides to specialized).
	rsLabelMap, err := metav1.LabelSelectorAsMap(rs.Spec.Selector)
	if err != nil {
		logger.Error(err, "Failed to parse label selector")
		return nil // malformed selector cannot succeed on retry
	}
	rsLabelMap["managed"] = "false"
	podList := &v1.PodList{}
	if err := p.gpm.crClient.List(ctx, podList, client.InNamespace(rs.Namespace), client.MatchingLabels(rsLabelMap)); err != nil {
		return fmt.Errorf("list specialized pods for RS %s/%s: %w", rs.Namespace, rs.Name, err)
	}
	if len(podList.Items) == 0 {
		return nil
	}
	clean, err := p.shouldCleanupForRS(ctx, rs, logger)
	if err != nil {
		return err
	}
	if !clean {
		logger.Info("pool RS rolled without an environment change; keeping specialized pods",
			"numPods", len(podList.Items))
		return nil
	}
	logger.Info("specialized pods identified for cleanup with RS", "numPods", len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
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
	return nil
}

// shouldCleanupForRS decides teardown-or-env-change (clean) vs
// executor-side-roll (keep) for a zero-replica pool RS.
func (p *PoolPodController) shouldCleanupForRS(ctx context.Context, rs *apps.ReplicaSet, logger logr.Logger) (bool, error) {
	ownerRef := metav1.GetControllerOf(rs)
	if ownerRef == nil || !strings.EqualFold(ownerRef.Kind, "Deployment") {
		return true, nil // orphan RS: legacy behavior
	}
	// Direct client, deliberately: the executor Manager cache's Deployment
	// watch is label-bounded to newdeploy/container, so a cache Get of a
	// poolmgr pool Deployment ALWAYS returns NotFound — which this path
	// would read as "teardown" and delete pods that must survive.
	dep, err := p.kubernetesClient.AppsV1().Deployments(rs.Namespace).Get(ctx, ownerRef.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil // owner gone: pool destroyed
	}
	if err != nil {
		return false, fmt.Errorf("get pool Deployment %s/%s: %w", rs.Namespace, ownerRef.Name, err)
	}
	if dep.DeletionTimestamp != nil || dep.UID != ownerRef.UID {
		return true, nil // owner deleting, or a new Deployment reused the name
	}

	// The owner is a live pool mid-roll. Recycle iff the ENVIRONMENT moved.
	tmpl := rs.Spec.Template.Labels
	envName, envNS := tmpl[fv1.ENVIRONMENT_NAME], tmpl[fv1.ENVIRONMENT_NAMESPACE]
	if envName == "" || envNS == "" {
		return true, nil // unrecognized template shape: legacy behavior
	}
	env := &fv1.Environment{}
	err = p.gpm.crClient.Get(ctx, client.ObjectKey{Namespace: envNS, Name: envName}, env)
	if apierrors.IsNotFound(err) {
		// Destructive decision on a cache miss: verify uncached first — the
		// same stale-cache guard the Environment reconciler applies before
		// CleanupEnvironment (a watch reconnect can transiently report
		// NotFound for an object that exists).
		if _, aerr := p.gpm.fissionClient.CoreV1().Environments(envNS).Get(ctx, envName, metav1.GetOptions{}); aerr != nil {
			if apierrors.IsNotFound(aerr) {
				return true, nil // env genuinely gone: teardown
			}
			return false, fmt.Errorf("verify environment %s/%s uncached: %w", envNS, envName, aerr)
		}
		// Requeue via error, deliberately: the RS reconciler is registered
		// with GenerationChangedPredicate, so a settled zero-replica RS emits
		// no further events and returning (false, nil) here would FORGET the
		// decision — stale-runtime pods would keep serving until the executor
		// next restarts. The error path is the only re-visit mechanism.
		return false, fmt.Errorf("environment %s/%s NotFound in cache but live in API server (stale cache); requeueing the decision", envNS, envName)
	}
	if err != nil {
		return false, fmt.Errorf("get environment %s/%s: %w", envNS, envName, err)
	}
	if _, hasHash := tmpl[fv1.ENVIRONMENT_RUNTIME_HASH]; !hasHash {
		// Pre-label RS (born under a release without the hash label): KEEP,
		// unconditionally — that is what makes warm pods survive the very
		// upgrade that ships the labels.
		//
		// Do NOT sharpen this with "clean when the live Deployment's template
		// already carries the label". That clause shipped once and killed the
		// exact pods it was reviewed to protect: the executor's own
		// hash-stamping update is what ROLLS the pool, so by the time the old
		// RS reports zero replicas its owner ALWAYS carries the label — the
		// "not yet re-templated" window does not exist (the upgrade gate
		// measured warm_pod_survival 1 -> 0 on both legs). It would also
		// re-fire on every later executor restart: zero-replica RSes are
		// retained by the Deployment's revision history and replay through
		// this reconciler on informer sync, condemning the survivors then.
		//
		// The cost of the plain keep is the documented one-release caveat
		// (RELEASES.md): a pre-label pod whose environment changed while the
		// executor was down is recycled by the idle reaper or the next
		// function update rather than here.
		return false, nil
	}
	// Recycle iff the template's birth runtime is no longer the live one: a
	// same-name recreate, or an env change that touched the template inputs.
	// Shared with the adopt pass (envLabelsMatchLiveEnv) so the two paths
	// cannot disagree about which pods deserve to live.
	return !envLabelsMatchLiveEnv(tmpl, env), nil
}

// cleanupSpecializedPodsForEnv enqueues an environment's specialized pods for
// cleanup. Called by the Environment reconciler on delete (via the gpm actor's
// cleanupEnvPool), after the warm pool itself has been destroyed.
func (p *PoolPodController) cleanupSpecializedPodsForEnv(ctx context.Context, env *fv1.Environment) {
	specializePodLabels := getSpecializedPodLabels(env)
	ns := p.nsResolver.ResolveNamespace(p.nsResolver.FunctionNamespace)
	podList := &v1.PodList{}
	if err := p.gpm.crClient.List(ctx, podList, client.InNamespace(ns), client.MatchingLabels(specializePodLabels)); err != nil {
		p.logger.Error(err, "failed to list specialized pods")
		return
	}
	if len(podList.Items) == 0 {
		return
	}
	p.logger.Info("specialized pods identified for cleanup after env delete", "env", env.Name, "namespace", env.Namespace, "count", len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
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
	// Pod reads go through the Manager cache, which is synced before this Runnable
	// starts, so there is no informer cache to wait for here.
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
	pod := &v1.Pod{}
	err = p.gpm.crClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pod)
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
