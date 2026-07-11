// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/metrics"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// createSvc creates the legacy per-function ClusterIP Service used by the
// optional useSvc mode (selector-based, fronting the specialized pod).
func (gp *GenericPool) createSvc(ctx context.Context, name string, labels map[string]string) (*apiv1.Service, error) {
	otelUtils.SpanTrackEvent(ctx, "createSvc", otelUtils.MapToAttributes(map[string]string{
		"name": name,
	})...)
	service := apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: apiv1.ServiceSpec{
			Type: apiv1.ServiceTypeClusterIP,
			Ports: []apiv1.ServicePort{
				{
					Protocol:   apiv1.ProtocolTCP,
					Port:       svcinfo.PortEnvRuntime,
					TargetPort: intstr.FromInt(svcinfo.PortEnvRuntime),
				},
			},
			Selector: labels,
		},
	}
	svc, err := gp.kubernetesClient.CoreV1().Services(gp.fnNamespace).Create(ctx, &service, metav1.CreateOptions{})
	return svc, err
}

// functionServicesEnabled reads the RFC-0002 gate (ENABLE_FUNCTION_SERVICES,
// Helm executor.functionServices.enabled); unset or empty means off. An
// unparsable value also means off but is logged once: silently disabling one
// half of the data-plane cutover over a typo would be invisible otherwise
// (the router side hard-fails on a bad mode; the executor must keep running
// because the legacy path still serves).
func functionServicesEnabled() bool {
	raw := os.Getenv("ENABLE_FUNCTION_SERVICES")
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		warnBadGateOnce.Do(func() {
			loggerfactory.GetLogger().WithName("poolmgr").Error(err,
				"unparsable ENABLE_FUNCTION_SERVICES; treating as false", "value", raw)
		})
		return false
	}
	return enabled
}

// warnBadGateOnce dedups the unparsable-gate warning (the gate is read on
// every ensure).
var warnBadGateOnce sync.Once

// functionServiceName returns the deterministic name of a function's headless
// Service (RFC-0002): fn-<name>-<uid8>, truncated to fit the 63-char Service
// name limit. uid8 is the first 8 hex chars of sha256(uid) so the name stays
// stable for the function's lifetime and unique across delete/recreate.
func functionServiceName(fn *fv1.Function) string {
	h := sha256.Sum256([]byte(fn.UID))
	uid8 := hex.EncodeToString(h[:])[:8]
	name := fn.Name
	// "fn-" + name + "-" + uid8 must fit in 63 chars.
	if max := 63 - len("fn-") - len("-")*1 - len(uid8); len(name) > max {
		name = name[:max]
	}
	return fmt.Sprintf("fn-%s-%s", name, uid8)
}

// functionServiceSelector matches exactly the pods specialized for this
// function at its current generation that have completed specialization
// (RFC-0002): the fission.io/served gate keeps relabeled-but-unspecialized
// pods out of the EndpointSlices, and the generation label keeps
// stale-generation pods out after a function update.
func functionServiceSelector(fn *fv1.Function) map[string]string {
	return map[string]string{
		fv1.FUNCTION_UID:        string(fn.UID),
		fv1.FUNCTION_GENERATION: strconv.FormatInt(fn.Generation, 10),
		fv1.SERVED_LABEL:        fv1.SERVED_VALUE,
	}
}

// ensureFunctionService idempotently creates (or updates the selector of) the
// function's headless Service in the pool namespace, so the built-in
// EndpointSlice controller publishes the function's specialized pods to the
// router's slice-fed endpoint index. Headless (clusterIP: None): the router
// dials pod IPs directly, and headless avoids kube-proxy programming rules on
// every node for per-function Services.
//
// Never called on the synchronous cold-start path — see
// ensureFunctionServiceAsync.
func (gpm *GenericPoolManager) ensureFunctionService(ctx context.Context, fn *fv1.Function) error {
	ns := gpm.nsResolver.GetFunctionNS(fn.Namespace)
	name := functionServiceName(fn)
	selector := functionServiceSelector(fn)

	desired := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				fv1.MANAGED_BY_LABEL:   fv1.MANAGED_BY_VALUE,
				fv1.EXECUTOR_TYPE:      string(fv1.ExecutorTypePoolmgr),
				fv1.FUNCTION_NAME:      fn.Name,
				fv1.FUNCTION_NAMESPACE: fn.Namespace,
				fv1.FUNCTION_UID:       string(fn.UID),
			},
			Annotations: map[string]string{
				fv1.EXECUTOR_INSTANCEID_LABEL: gpm.instanceID,
			},
		},
		Spec: apiv1.ServiceSpec{
			ClusterIP: apiv1.ClusterIPNone,
			Ports: []apiv1.ServicePort{
				{
					Protocol:   apiv1.ProtocolTCP,
					Port:       svcinfo.PortEnvRuntime,
					TargetPort: intstr.FromInt(svcinfo.PortEnvRuntime),
				},
			},
			Selector: selector,
		},
	}
	// An owner reference enables cascade-delete on Function deletion, but k8s
	// forbids cross-namespace owner refs — when the pool namespace differs from
	// the function namespace (FISSION_FUNCTION_NAMESPACE installs), cleanup
	// falls to deleteFunctionService + the instanceID reaper instead.
	// NewControllerRef matches the construction every other Function-owned
	// resource uses (incl. BlockOwnerDeletion for foreground cascades).
	if utils.IsOwnerReferencesEnabled() && fn.Namespace == ns {
		desired.OwnerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(fn, fv1.SchemeGroupVersion.WithKind("Function")),
		}
	}

	existing, err := gpm.kubernetesClient.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if kerrors.IsNotFound(err) {
		_, err = gpm.kubernetesClient.CoreV1().Services(ns).Create(ctx, desired, metav1.CreateOptions{})
		if kerrors.IsAlreadyExists(err) {
			// Lost a create race with a concurrent ensure — the winner's object
			// is identical (deterministic spec), nothing left to do.
			return nil
		}
		if err == nil {
			metrics.RecordFunctionServiceEnsure(ctx, "created")
		}
		return err
	}
	if err != nil {
		return err
	}

	// Update only on drift (selector tracks fn.Generation; instanceID tracks
	// executor restarts) so a steady-state ensure is read-only.
	if equality.Semantic.DeepEqual(existing.Spec.Selector, selector) &&
		existing.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] == gpm.instanceID {
		metrics.RecordFunctionServiceEnsure(ctx, "exists")
		return nil
	}
	updated := existing.DeepCopy()
	updated.Spec.Selector = selector
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] = gpm.instanceID
	_, err = gpm.kubernetesClient.CoreV1().Services(ns).Update(ctx, updated, metav1.UpdateOptions{})
	if err == nil {
		metrics.RecordFunctionServiceEnsure(ctx, "updated")
	}
	return err
}

// fnSvcEnsureDebounce bounds how often maybeEnsureFunctionService re-runs the
// (read-mostly) ensure per function.
const fnSvcEnsureDebounce = 30 * time.Second

// maybeEnsureFunctionService fires an async, debounced ensure of the
// function's headless Service. Called from both the cold-start path (first
// creation) and the warm RPC cache-hit path: the latter is the self-healing
// loop — a lost ensure (executor rolled mid-flight) leaves the function
// without slices, which routes all its traffic through the RPC path, which
// re-triggers the ensure here. Debounced per function UID so steady-state
// traffic adds no API reads; skipped for OnceOnly functions, whose pods serve
// exactly one request and must never be admitted from slices.
func (gpm *GenericPoolManager) maybeEnsureFunctionService(fn *fv1.Function) {
	if !gpm.functionServicesEnabled || fn.Spec.OnceOnly {
		return
	}
	if v, ok := gpm.fnSvcEnsured.Load(fn.UID); ok {
		if last, ok := v.(time.Time); ok && time.Since(last) < fnSvcEnsureDebounce {
			return
		}
	}
	// Optimistic stamp dedups concurrent triggers; the failure path below
	// removes it so the next request retries immediately.
	gpm.fnSvcEnsured.Store(fn.UID, time.Now())
	go gpm.ensureFunctionServiceAsync(fn)
}

// ensureFunctionServiceAsync runs ensureFunctionService off the cold-start
// path: fire-and-forget with its own detached timeout context and one retry.
// Errors are logged and counted, never surfaced to the invoking request — the
// pod IP has already been returned, and the next request re-ensures (the
// debounce stamp is dropped on failure).
func (gpm *GenericPoolManager) ensureFunctionServiceAsync(fn *fv1.Function) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := gpm.ensureFunctionService(ctx, fn)
	if err == nil {
		return
	}
	gpm.logger.V(1).Info("retrying function service ensure", "function", fn.Name, "namespace", fn.Namespace, "error", err.Error())
	time.Sleep(2 * time.Second)
	if err := gpm.ensureFunctionService(ctx, fn); err != nil {
		gpm.fnSvcEnsured.Delete(fn.UID)
		metrics.RecordFunctionServiceEnsure(ctx, "error")
		gpm.logger.Error(err, "failed to ensure function service; warm-path endpoint discovery degrades to executor RPC for this function",
			"function", fn.Name, "namespace", fn.Namespace)
	}
}

// deleteFunctionService removes the function's headless Service. Idempotent (a
// missing Service is success). Driven by the Function reconciler on delete —
// the owner reference covers same-namespace installs, this covers
// cross-namespace ones and keeps both paths symmetric.
func (gpm *GenericPoolManager) deleteFunctionService(ctx context.Context, fn *fv1.Function) error {
	ns := gpm.nsResolver.GetFunctionNS(fn.Namespace)
	err := gpm.kubernetesClient.CoreV1().Services(ns).Delete(ctx, functionServiceName(fn), metav1.DeleteOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return err
	}
	return nil
}
