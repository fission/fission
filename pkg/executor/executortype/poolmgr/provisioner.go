// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/metrics"
	"github.com/fission/fission/pkg/executor/util"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

// ProvisionerConfig configures the RFC-0026 provisioner.
type ProvisionerConfig struct {
	// MaxPerFunction is the namespace-wide cap on ProvisionedConcurrencyConfig.Target.
	// The webhook (or CEL, if we add a namespace-scoped cap later) enforces it;
	// the provisioner also clamps the effective target to this value as
	// defense-in-depth.
	MaxPerFunction int

	// MaxInflightPerFunction bounds the number of concurrent eager
	// specializations per function. Prevents head-of-line blocking: a warm-up
	// burst must not saturate EXECUTOR_SPECIALIZATION_CONCURRENCY and starve
	// on-demand cold starts for other functions (invariant P5).
	MaxInflightPerFunction int

	// ReconcileInterval is how often the provisioner scans opted-in functions
	// and reconciles their warm-pod count toward the effective target.
	ReconcileInterval time.Duration
}

// Provisioner maintains warm specialized pods for functions that opt into
// ProvisionedConcurrency (RFC-0026). It runs a periodic reconcile loop that
// for each opted-in function:
//  1. Computes the effective target (base Target only in PR 1; schedule
//     windows arrive in PR 2).
//  2. Counts ready provisioned pods (Kubernetes API list, not fsCache —
//     see countProvisionedPods below).
//  3. If below target: eagerly specializes delta pods via gpm.GetFuncSvc,
//     paced by MaxInflightPerFunction. Each successfully specialized pod
//     gets the fission.io/provisioned label (reaper exemption).
//  4. If above target (target dropped): clears fission.io/provisioned from
//     excess pods so the idle reaper can retire them (invariant P4: no
//     immortal pods).
//  5. Updates FunctionStatus.ProvisionedReady / ProvisionedTarget.
//
// The provisioner reuses gpm.GetFuncSvc — the full cold-start flow — so eager
// pods are byte-identical to on-demand pods: same cache entries, same headless
// Service membership, same router EndpointSlice visibility. Zero router changes.
type Provisioner struct {
	logger           logr.Logger
	gpm              *GenericPoolManager
	fissionClient    versioned.Interface
	kubernetesClient kubernetes.Interface
	crClient         client.Client
	config           ProvisionerConfig

	// inflight tracks the number of in-flight eager specializations per
	// function UID. Used by tryAcquire/release to pace warm-up and prevent
	// head-of-line blocking (invariant P5).
	inflight sync.Map // map[types.UID]*atomic.Int32
}

// ProvisionerConfigFromEnv build ProvisionerConfig from
// EXECUTOR_PROVISIONED_* env vars.
// Returns zero ProvisionerConfig when EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED is unset/false
func ProvisionerConfigFromEnv() (ProvisionerConfig, bool) {
	enabled, err := strconv.ParseBool(os.Getenv("EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED"))
	if !enabled || err != nil {
		return ProvisionerConfig{}, false
	}
	cfg := ProvisionerConfig{
		MaxPerFunction:         util.AtoiOr("EXECUTOR_PROVISIONED_MAX_PER_FUNCTION", 20),
		MaxInflightPerFunction: util.AtoiOr("EXECUTOR_PROVISIONED_MAX_INFLIGHT_PER_FUNCTION", 4),
		ReconcileInterval:      util.DurOr("EXECUTOR_PROVISIONED_RECONCILE_INTERVAL", 30*time.Second),
	}
	return cfg, true
}

func NewProvisioner(
	logger logr.Logger,
	gpm *GenericPoolManager,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	crClient client.Client,
	config ProvisionerConfig,
) *Provisioner {
	return &Provisioner{
		logger:           logger,
		gpm:              gpm,
		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		crClient:         crClient,
		config:           config,
		inflight:         sync.Map{},
	}
}

// Run starts the provisioner's reconcile loop. A non-positive
// ReconcileInterval is clamped to 30s to avoid time.NewTicker panic.
func (p *Provisioner) Run(ctx context.Context) {
	interval := p.config.ReconcileInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	p.reconcileAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reconcileAll(ctx)
		}
	}
}

func filterOptedFunctions(fnlist *fv1.FunctionList) []fv1.Function {
	var opted []fv1.Function
	for _, fn := range fnlist.Items {
		et := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
		if fn.Spec.ProvisionedConcurrency != nil && (et == fv1.ExecutorTypePoolmgr || et == "") {
			opted = append(opted, fn)
		}
	}
	return opted
}

// lists all functions and filters only those with
// fn.Spec.ProvisionedConcurrency and
// fn.Spec.InvokeStragey.ExecutionStrategy.ExecutorType==poolmgr
func (p *Provisioner) reconcileAll(ctx context.Context) {
	fnList := &fv1.FunctionList{}
	// list all functions
	err := p.crClient.List(ctx, fnList)
	if err != nil {
		p.logger.Error(err, "Unable to fetch functions")
		return
	}
	// filter only functions which have ProvisionedConcurrency and ExecutorType=poolmgr
	opted := filterOptedFunctions(fnList)

	// if no functions have opted in, return immediately.
	if len(opted) == 0 {
		return
	}

	const maxConcurrentReconciles = 10 // TODO: make this configurable
	sem := make(chan struct{}, maxConcurrentReconciles)
	var wg sync.WaitGroup
	for _, fn := range opted {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			p.reconcileFunction(ctx, &fn)
		})
	}
	wg.Wait()
}

func (p *Provisioner) reconcileFunction(ctx context.Context, fn *fv1.Function) {
	target := p.effectiveTarget(fn)
	if target == 0 {
		p.clearAllProvisionedLabels(ctx, fn)
		err := p.updateFunctionStatus(ctx, fn, 0, 0)
		if err != nil {
			p.logger.Error(err, "Unable to update function status", "function", fn.Name, "namespace", fn.Namespace)
		}
		return
	}

	ready, err := p.countProvisionedPods(ctx, fn)
	if err != nil {
		p.logger.Error(err, "Unable to get count of provisioned pods", "function", fn.Name, "namespace", fn.Namespace)
		return
	}
	if ready < target {
		delta := target - ready
		p.fireEagerSpecializations(ctx, fn, delta)
	} else if ready > target {
		excess := ready - target
		p.clearExcessProvisionedLabels(ctx, fn, excess)
	}

	// Publish observed status on every pass so ProvisionedReady and the
	// Provisioned=Warming condition surface during warm-up/drain, not only
	// once ready == target.
	if err := p.updateFunctionStatus(ctx, fn, ready, target); err != nil {
		p.logger.Error(err, "Unable to update status of the function", "function", fn.Name, "namespace", fn.Namespace, "ready", ready, "target", target)
	}
}

func (p *Provisioner) listPods(ctx context.Context, fn *fv1.Function) (corev1.PodList, error) {
	podList := corev1.PodList{}
	labelMap := map[string]string{}
	labelMap[fv1.FUNCTION_UID] = string(fn.UID)
	labelMap[fv1.SERVED_LABEL] = fv1.SERVED_VALUE
	labelMap[fv1.PROVISIONED_LABEL] = fv1.PROVISIONED_VALUE
	err := p.crClient.List(
		ctx,
		&podList,
		client.MatchingLabels(labelMap),
	)
	if err != nil {
		return podList, err
	}
	return podList, nil
}

func (p *Provisioner) clearProvisionedLabel(ctx context.Context, pod *corev1.Pod) error {
	patch := fmt.Sprintf(`{"metadata":{"labels":{"%s":null}}}`, fv1.PROVISIONED_LABEL)
	_, err := p.kubernetesClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

func (p *Provisioner) clearExcessProvisionedLabels(ctx context.Context, fn *fv1.Function, excess int) {
	podList, err := p.listPods(ctx, fn)
	if err != nil {
		p.logger.Error(err, "Unable to list pods", "function", fn.Name, "namespace", fn.Namespace)
		return
	}
	// sort pods by creation time and delete the oldest ones
	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[i].CreationTimestamp.Before(&podList.Items[j].CreationTimestamp)
	})
	for i := 0; i < excess && i < len(podList.Items); i++ {
		err := p.clearProvisionedLabel(ctx, &podList.Items[i])
		if err != nil {
			p.logger.Error(err, "unable to clear provisioned label", "pod", podList.Items[i].Name)
		}
	}
}

func (p *Provisioner) tryAcquire(fnUID types.UID) bool {
	v, _ := p.inflight.LoadOrStore(fnUID, new(atomic.Int32))
	count := v.(*atomic.Int32)
	if count.Add(1) <= int32(p.config.MaxInflightPerFunction) {
		return true
	}
	count.Add(-1) // rollback: rejected acquire must not hold a slot
	return false
}

func (p *Provisioner) release(fnUID types.UID) {
	v, ok := p.inflight.Load(fnUID)
	if !ok {
		return
	}
	v.(*atomic.Int32).Add(-1)
}

func (p *Provisioner) fireEagerSpecializations(ctx context.Context, fn *fv1.Function, delta int) {
	for range delta {
		if !p.tryAcquire(fn.UID) {
			break
		}
		go func() {
			defer p.release(fn.UID)
			if err := p.eagerSpecialize(ctx, fn); err != nil {
				p.logger.Error(err, "eager specialization failed", "namespace", fn.Namespace, "function", fn.Name)
			}
		}()
	}
}

func (p *Provisioner) eagerSpecialize(ctx context.Context, fn *fv1.Function) error {
	funSvc, err := p.gpm.GetFuncSvc(ctx, fn)
	if err != nil {
		metrics.RecordEagerSpecialization(ctx, fn.Name, fn.Namespace, "error")
		return err
	}
	for _, obj := range funSvc.KubernetesObjects {
		// gp.go builds kubeObjRefs with Kind "pod" (lowercase), so match
		// case-insensitively rather than relying on canonical capitalization.
		if strings.EqualFold(obj.Kind, "Pod") {
			patch := fmt.Sprintf(`{"metadata":{"labels":{"%s":"%s"}}}`,
				fv1.PROVISIONED_LABEL, fv1.PROVISIONED_VALUE)
			_, err := p.kubernetesClient.CoreV1().Pods(obj.Namespace).Patch(
				ctx, obj.Name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{},
			)
			if err == nil {
				metrics.RecordEagerSpecialization(ctx, fn.Name, fn.Namespace, "success")
			}
			if err != nil {
				// Pod is specialized and serving, just not reaper-exempt.
				// Next tick will see it as a non-provisioned served pod and
				// may re-specialize. Accept the race (design §5j).
				p.logger.Error(err, "provisioned label patch failed (pod is serving, not exempt)", "pod", obj.Name)
				metrics.RecordEagerSpecialization(ctx, fn.Name, fn.Namespace, "error")
			}
		}
	}
	return nil
}

func (p *Provisioner) countProvisionedPods(ctx context.Context, fn *fv1.Function) (int, error) {
	podList, err := p.listPods(ctx, fn)
	if err != nil {
		return 0, err
	}
	readyAndRunningPods := utils.ReadyAndRunningPodsFilter(&podList)
	if len(readyAndRunningPods) < len(podList.Items) {
		p.logger.V(1).Info("provisioned pod count: some pods not ready+running",
			"totalPods", len(podList.Items),
			"readyAndRunning", len(readyAndRunningPods),
			"function", fn.Name)
	}
	return len(readyAndRunningPods), nil
}

func statusSet(latestFunc *fv1.Function, ready, target int) {
	latestFunc.Status.ProvisionedReady = ready
	latestFunc.Status.ProvisionedTarget = target
	if target == 0 {
		// Provisioned concurrency is off (target=0): condition False,
		// regardless of ready count (which should also be 0).
		meta.SetStatusCondition(&latestFunc.Status.Conditions, metav1.Condition{
			Type:               fv1.FunctionConditionProvisioned,
			Status:             metav1.ConditionFalse,
			Reason:             fv1.FunctionReasonProvisionedDisabled,
			ObservedGeneration: latestFunc.Generation,
		})
		return
	}
	if ready >= target {
		meta.SetStatusCondition(&latestFunc.Status.Conditions, metav1.Condition{
			Type:               fv1.FunctionConditionProvisioned,
			Status:             metav1.ConditionTrue,
			Reason:             fv1.FunctionReasonProvisionedSatisfied,
			ObservedGeneration: latestFunc.Generation,
		})
	} else {
		meta.SetStatusCondition(&latestFunc.Status.Conditions, metav1.Condition{
			Type:               fv1.FunctionConditionProvisioned,
			Status:             metav1.ConditionFalse,
			Reason:             fv1.FunctionReasonProvisionedWarming,
			ObservedGeneration: latestFunc.Generation,
		})
	}
}

func (p *Provisioner) updateFunctionStatus(ctx context.Context, fn *fv1.Function, ready, target int) error {
	metrics.RecordProvisionedTarget(ctx, fn.Name, fn.Namespace, int64(target))
	metrics.RecordProvisionedReady(ctx, fn.Name, fn.Namespace, int64(ready))
	backoff := retry.DefaultRetry
	return retry.RetryOnConflict(backoff, func() error {
		latestFunc, err := p.fissionClient.CoreV1().Functions(fn.Namespace).Get(ctx, fn.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		statusSet(latestFunc, ready, target)
		_, err = p.fissionClient.CoreV1().Functions(fn.Namespace).UpdateStatus(ctx, latestFunc, metav1.UpdateOptions{})
		return err
	})
}

func (p *Provisioner) clearAllProvisionedLabels(ctx context.Context, fn *fv1.Function) {
	podList, err := p.listPods(ctx, fn)
	if err != nil {
		p.logger.Error(err, "Unable to list pods", "function", fn.Name, "namespace", fn.Namespace)
		return
	}
	if len(podList.Items) == 0 {
		return
	}

	for _, pod := range podList.Items {
		err := p.clearProvisionedLabel(ctx, &pod)
		if err != nil {
			p.logger.Error(err, "unable to clear provisioned label", "pod", pod.Name)
		}
	}
}

// computes the effectiveTarget: minimum of provisioned concurreny target
// and max per function. The schedule will be added in PR2
func (p *Provisioner) effectiveTarget(fn *fv1.Function) int {
	return min(fn.Spec.ProvisionedConcurrency.Target, p.config.MaxPerFunction)
}

func (p *Provisioner) StopProvisioning(ctx context.Context, fn *fv1.Function) {
	p.clearAllProvisionedLabels(ctx, fn)
	p.inflight.Delete(fn.UID)
}

// UpdateFunctionStatusZero resets the provisioned status fields to 0 and
// marks the Provisioned condition False. Called when provisioned concurrency
// is removed from the spec (target set to 0 / field nil).
func (p *Provisioner) UpdateFunctionStatusZero(ctx context.Context, fn *fv1.Function) error {
	return p.updateFunctionStatus(ctx, fn, 0, 0)
}
