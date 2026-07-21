// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package idle consolidates the executor's idle-pod reaping. It replaces the
// three near-identical idleObjectReaper goroutines (poolmgr, newdeploy,
// container) with one leader-only Runnable that drives a per-executor-type
// Strategy. Each strategy ticks on its own interval; every strategy's reaps are
// bounded to maxConcurrentReaps (previously only poolmgr was bounded — a
// traffic drop could otherwise spawn one goroutine per idle deployment).
package idle

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apiv1 "k8s.io/api/core/v1"
	k8sErrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/reaper"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

// maxConcurrentReaps bounds in-flight reaps per strategy: a traffic drop can
// leave thousands of function services idle at once, and one reap goroutine
// each would spike goroutines and hammer the API server.
const maxConcurrentReaps = 10

// idleListAge is the coarse pre-filter the cache applies before a strategy's
// per-function idle threshold is evaluated.
const idleListAge = 5 * time.Second

// Strategy is one executor type's idle-reaping behaviour.
type Strategy interface {
	// Name identifies the strategy in logs.
	Name() string
	// ExecutorType is the executor type whose function services this strategy
	// reaps; the Reaper filters candidates to this type.
	ExecutorType() fv1.ExecutorType
	// Interval is how often this strategy runs.
	Interval() time.Duration
	// ListIdle returns the candidate function services (those idle past the
	// coarse cache pre-filter).
	ListIdle() ([]*fscache.FuncSvc, error)
	// Prepare runs once per tick before any candidate is processed. Returning an
	// error skips the whole tick — used when an incomplete environment list
	// could otherwise reap live objects.
	Prepare(ctx context.Context) error
	// Reap evaluates one candidate (already filtered to ExecutorType) and
	// performs the idle action if it is past its per-function idle threshold and
	// not otherwise skipped (websocket, Infinite, deleted function, ...).
	Reap(ctx context.Context, fsvc *fscache.FuncSvc) error
}

// Reaper periodically reaps idle function services across all strategies.
type Reaper struct {
	logger     logr.Logger
	strategies []Strategy
}

// NewReaper builds a reaper over the given strategies.
func NewReaper(logger logr.Logger, strategies ...Strategy) *Reaper {
	return &Reaper{logger: logger.WithName("idle_reaper"), strategies: strategies}
}

// Start launches one ticker per strategy and blocks until ctx is cancelled
// (leadership loss or shutdown); run it under an errgroup via a closure.
func (r *Reaper) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range r.strategies {
		wg.Go(func() {
			r.logger.Info("starting idle reaper", "strategy", s.Name(), "interval", s.Interval())
			wait.UntilWithContext(ctx, func(ctx context.Context) { r.reapOnce(ctx, s) }, s.Interval())
		})
	}
	wg.Wait()
}

func (r *Reaper) reapOnce(ctx context.Context, s Strategy) {
	if err := s.Prepare(ctx); err != nil {
		r.logger.Error(err, "skipping idle reaper pass", "strategy", s.Name())
		return
	}
	funcSvcs, err := s.ListIdle()
	if err != nil {
		r.logger.Error(err, "error listing idle function services", "strategy", s.Name())
		return
	}

	sem := make(chan struct{}, maxConcurrentReaps)
	var wg sync.WaitGroup
	for i := range funcSvcs {
		fsvc := funcSvcs[i]
		if fsvc.Executor != s.ExecutorType() {
			continue
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := s.Reap(ctx, fsvc); err != nil {
				r.logger.Error(err, "error reaping idle function service", "strategy", s.Name(), "function", fsvc.Function.Name)
			}
		})
	}
	wg.Wait()
}

// idleThreshold returns the per-function idle reap duration: the function's
// IdleTimeout if set, otherwise the strategy default.
func idleThreshold(fn *fv1.Function, def time.Duration) time.Duration {
	if fn != nil && fn.Spec.IdleTimeout != nil {
		return time.Duration(*fn.Spec.IdleTimeout) * time.Second
	}
	return def
}

// listEnvUIDs returns the set of Environment UIDs across the Fission resource
// namespaces, used to detect function services whose environment was deleted.
func listEnvUIDs(ctx context.Context, fissionClient versioned.Interface) (map[k8sTypes.UID]struct{}, error) {
	envUIDs := make(map[k8sTypes.UID]struct{})
	for _, namespace := range utils.DefaultNSResolver().FissionResourceNamespaces() {
		envs, err := fissionClient.CoreV1().Environments(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, env := range envs.Items {
			envUIDs[env.UID] = struct{}{}
		}
	}
	return envUIDs, nil
}

// listFunctionsByUID returns all functions across the Fission resource
// namespaces keyed by UID.
func listFunctionsByUID(ctx context.Context, fissionClient versioned.Interface) (map[k8sTypes.UID]fv1.Function, error) {
	fnByUID := make(map[k8sTypes.UID]fv1.Function)
	for _, namespace := range utils.DefaultNSResolver().FissionResourceNamespaces() {
		fns, err := fissionClient.CoreV1().Functions(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range fns.Items {
			fnByUID[fns.Items[i].UID] = fns.Items[i]
		}
	}
	return fnByUID, nil
}

func getDeploymentObj(kubeobjs []apiv1.ObjectReference) *apiv1.ObjectReference {
	for i := range kubeobjs {
		if kubeobjs[i].Kind == "Deployment" || kubeobjs[i].Kind == "deployment" {
			return &kubeobjs[i]
		}
	}
	return nil
}

func scaleDeploymentToMinScale(ctx context.Context, logger logr.Logger, kubeClient kubernetes.Interface, namespace, name string, replicas int32) error {
	logger.Info("scaling down idle deployment", "deployment", name, "namespace", namespace, "replicas", replicas)
	_, err := kubeClient.AppsV1().Deployments(namespace).UpdateScale(ctx, name, &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
	}, metav1.UpdateOptions{})
	return err
}

// PoolDeleteStrategy reaps poolmgr function services by deleting their idle
// pods (the generic-pool warm-pod path). It skips function services with an
// active websocket connection or an environment configured for an infinite
// number of functions per container.
type PoolDeleteStrategy struct {
	logger        logr.Logger
	fissionClient versioned.Interface
	fsCache       *fscache.FunctionServiceCache
	kubeClient    kubernetes.Interface
	reapTime      time.Duration
	interval      time.Duration

	// drainBeforeDelete enables the RFC-0002 two-step reap: remove the
	// fission.io/served label first (the pod leaves its function Service's
	// EndpointSlices, so every router stops admitting it within watch
	// latency), then delete after a drain grace. Gated on the executor's
	// function-Services flag — without slices there is nothing to drain from.
	drainBeforeDelete bool

	// Per-tick state populated by Prepare. Ticks for a strategy never overlap
	// (wait.UntilWithContext), and Reap goroutines only read these maps.
	envUIDs map[k8sTypes.UID]struct{}
	fnByUID map[k8sTypes.UID]fv1.Function
}

func NewPoolDeleteStrategy(logger logr.Logger, fissionClient versioned.Interface, fsCache *fscache.FunctionServiceCache, kubeClient kubernetes.Interface, reapTime, interval time.Duration, drainBeforeDelete bool) *PoolDeleteStrategy {
	return &PoolDeleteStrategy{
		logger:            logger,
		fissionClient:     fissionClient,
		fsCache:           fsCache,
		kubeClient:        kubeClient,
		reapTime:          reapTime,
		interval:          interval,
		drainBeforeDelete: drainBeforeDelete,
	}
}

func (s *PoolDeleteStrategy) Name() string                   { return string(fv1.ExecutorTypePoolmgr) }
func (s *PoolDeleteStrategy) ExecutorType() fv1.ExecutorType { return fv1.ExecutorTypePoolmgr }
func (s *PoolDeleteStrategy) Interval() time.Duration        { return s.interval }

func (s *PoolDeleteStrategy) ListIdle() ([]*fscache.FuncSvc, error) {
	return s.fsCache.ListOldForPool(idleListAge)
}

func (s *PoolDeleteStrategy) Prepare(ctx context.Context) error {
	envUIDs, err := listEnvUIDs(ctx, s.fissionClient)
	if err != nil {
		return err
	}
	fnByUID, err := listFunctionsByUID(ctx, s.fissionClient)
	if err != nil {
		return err
	}
	s.envUIDs, s.fnByUID = envUIDs, fnByUID
	return nil
}

func (s *PoolDeleteStrategy) Reap(ctx context.Context, fsvc *fscache.FuncSvc) error {
	if _, ok := s.fsCache.WebsocketFsvc.Load(fsvc.Name); ok {
		return nil
	}
	// Skip provisioned pods — the provisioner (RFC-0026) is actively
	// maintaining them. The provisioner clears fission.io/provisioned
	// before letting the reaper retire them (target drop, window close,
	// generation bump, spec deletion). Function-level check: if the
	// function opts into provisioned concurrency, all its specialized
	// pods are provisioner-managed. The narrow race between
	// GetFuncSvc returning and the provisioner's label patch completing
	// is accepted (design §5j) — the provisioner re-specializes on the
	// next tick.
	//
	// PR1 LIMITATION: this function-level exemption skips ALL specialized
	// pods of an opted-in function, not just the provisioned floor (target).
	// An opted-in function (target=2) that bursts to N on-demand pods (up
	// to Concurrency, default 500) keeps all N pods forever while PC is
	// enabled — the reaper never retires the overflow, and unlike the
	// label-patch race this does NOT self-heal (no per-pod signal
	// distinguishes floor from overflow). PR2 replaces this with a
	// per-pod-label check (fission.io/provisioned=true) so only floor pods
	// are exempt and overflow pods are reaped normally. Until then, the
	// overflow-retention cost is a known PR1 trade-off.
	if fn, ok := s.fnByUID[fsvc.Function.UID]; ok {
		if fn.Spec.ProvisionedConcurrency != nil {
			return nil
		}
	}

	// For a function whose environment no longer exists, reap the idle pod as
	// usual but log to notify the user.
	if _, ok := s.envUIDs[fsvc.Environment.UID]; !ok {
		s.logger.Error(nil, "function environment no longer exists", "environment", fsvc.Environment.Name, "function", fsvc.Name)
	}
	if fsvc.Environment.Spec.AllowedFunctionsPerContainer == fv1.AllowedFunctionsPerContainerInfinite {
		return nil
	}

	reapTime := s.reapTime
	if fn, ok := s.fnByUID[fsvc.Function.UID]; ok {
		reapTime = idleThreshold(&fn, s.reapTime)
	}
	if time.Since(fsvc.Atime) < reapTime {
		return nil
	}

	deleted, err := s.fsCache.DeleteOldPoolCache(ctx, fsvc, reapTime)
	if err != nil {
		return err
	}
	if deleted {
		if s.drainBeforeDelete {
			s.drainThenDelete(ctx, fsvc)
			return nil
		}
		for i := range fsvc.KubernetesObjects {
			s.logger.Info(
				"release idle function resources",
				"function", fsvc.Function.Name,
				"address", fsvc.Address,
				"executor", string(fsvc.Executor),
				"pod", fsvc.Name,
			)
			reaper.CleanupKubeObject(ctx, s.logger, s.kubeClient, &fsvc.KubernetesObjects[i])
			time.Sleep(50 * time.Millisecond)
		}
	}
	return nil
}

// drainGraceCap bounds the drain grace so functions with very long timeouts
// don't pin reaped pods for that long (RFC-0002 open question resolved: cap).
const (
	drainGraceMin = 30 * time.Second
	drainGraceCap = 5 * time.Minute
)

// drainThenDelete is the RFC-0002 two-step reap: unlabel now (the pod drops
// out of its function Service's EndpointSlices, and routers stop admitting it
// within watch latency), delete after a grace long enough for in-flight
// requests to finish. The delayed delete is detached (fire-and-forget): if the
// executor restarts before it fires, the orphaned pod is re-adopted into the
// fsCache by the adopt pass and reaped again on a later tick — self-healing,
// no leak.
func (s *PoolDeleteStrategy) drainThenDelete(ctx context.Context, fsvc *fscache.FuncSvc) {
	grace := drainGraceMin
	if fn, ok := s.fnByUID[fsvc.Function.UID]; ok {
		if t := time.Duration(fn.Spec.FunctionTimeout) * time.Second; t > grace {
			grace = t
		}
	}
	if grace > drainGraceCap {
		grace = drainGraceCap
	}

	// Strategic-merge null deletes the label; a no-op for pods that never
	// carried it.
	patch := fmt.Sprintf(`{"metadata":{"labels":{"%s":null}}}`, fv1.SERVED_LABEL)
	for i := range fsvc.KubernetesObjects {
		obj := fsvc.KubernetesObjects[i]
		if !strings.EqualFold(obj.Kind, "pod") {
			continue
		}
		_, err := s.kubeClient.CoreV1().Pods(obj.Namespace).Patch(ctx, obj.Name, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
		if err != nil && !k8sErrs.IsNotFound(err) {
			s.logger.Error(err, "error unlabeling idle pod for drain", "pod", obj.Name, "ns", obj.Namespace)
		}
	}
	s.logger.Info(
		"draining idle function resources before delete",
		"function", fsvc.Function.Name,
		"address", fsvc.Address,
		"pod", fsvc.Name,
		"grace", grace,
	)

	objs := fsvc.KubernetesObjects
	logger := s.logger
	kubeClient := s.kubeClient
	time.AfterFunc(grace, func() {
		dctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for i := range objs {
			logger.Info(
				"release drained idle function resources",
				"function", fsvc.Function.Name,
				"address", fsvc.Address,
				"pod", fsvc.Name,
			)
			// Bounded retry: the fsvc is already evicted from the cache, so
			// nothing in the running executor would ever retry a failed delete
			// — the unlabeled pod would leak until the next executor restart's
			// adopt pass. Three attempts ride out an apiserver blip.
			var derr error
			for attempt := range 3 {
				if attempt > 0 {
					time.Sleep(time.Duration(attempt) * 5 * time.Second)
				}
				if derr = reaper.DeleteKubeObject(dctx, kubeClient, &objs[i]); derr == nil {
					break
				}
			}
			if derr != nil {
				logger.Error(derr, "error deleting drained idle resource; pod leaks until the next executor restart",
					"type", objs[i].Kind, "name", objs[i].Name, "ns", objs[i].Namespace)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

// ScaleDownStrategy reaps newdeploy/container function services by scaling
// their deployment down to the function's MinScale. checkEnv mirrors the
// newdeploy behaviour of logging (and gating the tick on) the environment list;
// the container executor leaves it false.
type ScaleDownStrategy struct {
	logger        logr.Logger
	execType      fv1.ExecutorType
	fissionClient versioned.Interface
	fsCache       *fscache.FunctionServiceCache
	kubeClient    kubernetes.Interface
	reapTime      time.Duration
	interval      time.Duration
	checkEnv      bool

	envUIDs map[k8sTypes.UID]struct{} // per-tick, only when checkEnv
}

func NewScaleDownStrategy(logger logr.Logger, execType fv1.ExecutorType, fissionClient versioned.Interface, fsCache *fscache.FunctionServiceCache, kubeClient kubernetes.Interface, reapTime, interval time.Duration, checkEnv bool) *ScaleDownStrategy {
	return &ScaleDownStrategy{
		logger:        logger,
		execType:      execType,
		fissionClient: fissionClient,
		fsCache:       fsCache,
		kubeClient:    kubeClient,
		reapTime:      reapTime,
		interval:      interval,
		checkEnv:      checkEnv,
	}
}

func (s *ScaleDownStrategy) Name() string                   { return string(s.execType) }
func (s *ScaleDownStrategy) ExecutorType() fv1.ExecutorType { return s.execType }
func (s *ScaleDownStrategy) Interval() time.Duration        { return s.interval }

func (s *ScaleDownStrategy) ListIdle() ([]*fscache.FuncSvc, error) {
	return s.fsCache.ListOld(idleListAge)
}

func (s *ScaleDownStrategy) Prepare(ctx context.Context) error {
	if !s.checkEnv {
		return nil
	}
	envUIDs, err := listEnvUIDs(ctx, s.fissionClient)
	if err != nil {
		return err
	}
	s.envUIDs = envUIDs
	return nil
}

func (s *ScaleDownStrategy) Reap(ctx context.Context, fsvc *fscache.FuncSvc) error {
	fn, err := s.fissionClient.CoreV1().Functions(fsvc.Function.Namespace).Get(ctx, fsvc.Function.Name, metav1.GetOptions{})
	if err != nil {
		// The deploy managers handle the function delete event and clean up the
		// cache/objects themselves, so a missing function is not an error here.
		if k8sErrs.IsNotFound(err) {
			return nil
		}
		return err
	}

	if s.checkEnv {
		if _, ok := s.envUIDs[fsvc.Environment.UID]; !ok {
			s.logger.Info("function environment no longer exists", "environment", fsvc.Environment.Name, "function", fsvc.Name)
		}
	}

	if time.Since(fsvc.Atime) < idleThreshold(fn, s.reapTime) {
		return nil
	}

	deployObj := getDeploymentObj(fsvc.KubernetesObjects)
	if deployObj == nil {
		return fmt.Errorf("no deployment found in kubernetes objects for function %q", fsvc.Function.Name)
	}
	currentDeploy, err := s.kubeClient.AppsV1().Deployments(deployObj.Namespace).Get(ctx, deployObj.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	minScale := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)
	// Do nothing if the current replica count is already at or below MinScale.
	if currentDeploy.Spec.Replicas != nil && *currentDeploy.Spec.Replicas <= minScale {
		return nil
	}
	return scaleDeploymentToMinScale(ctx, s.logger, s.kubeClient, deployObj.Namespace, deployObj.Name, minScale)
}
