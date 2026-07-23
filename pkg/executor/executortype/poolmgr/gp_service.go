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
	"regexp"
	"strconv"
	"sync"
	"time"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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

// versionSuffixPattern matches the "-v<seq>" tail of a FunctionVersion's
// name (minted as "<fn>-v<sequence>" by versioning.Publish).
var versionSuffixPattern = regexp.MustCompile(`-v[0-9]+$`)

// versionServiceSuffix derives the bounded suffix functionServiceName adds
// for a published version: the version label's own "-v<seq>" tail when it
// matches the expected shape, or a short deterministic hash-derived
// fallback otherwise -- so a version label that (by bug or hand-edit)
// doesn't end in "-v<seq>" can never blow the Service name's 63-char budget
// open-ended. Either way the result is a handful of bytes, bounded
// independent of the label's own length.
func versionServiceSuffix(versionLabel string) string {
	if m := versionSuffixPattern.FindString(versionLabel); m != "" {
		return m
	}
	h := sha256.Sum256([]byte(versionLabel))
	return "-v" + hex.EncodeToString(h[:])[:8]
}

// functionServiceName returns the deterministic name of a function's headless
// Service (RFC-0002): fn-<name>-<uid8>, truncated to fit the 63-char Service
// name limit. uid8 is the first 8 hex chars of sha256(uid) so the name stays
// stable for the function's lifetime and unique across delete/recreate.
//
// A versioned Function (fv1.FUNCTION_VERSION label present, RFC-0025) gets
// its own Service, distinguishable from the unversioned one and from every
// other version's: fn-<name>-<uid8>-<versionSuffix>, where versionSuffix is
// the bounded tail from versionServiceSuffix. name is truncated as needed to
// keep the whole name within 63 chars regardless of how long that suffix is.
func functionServiceName(fn *fv1.Function) string {
	h := sha256.Sum256([]byte(fn.UID))
	uid8 := hex.EncodeToString(h[:])[:8]

	suffix := ""
	if v := fn.Labels[fv1.FUNCTION_VERSION]; v != "" {
		suffix = versionServiceSuffix(v)
	}

	name := fn.Name
	// "fn-" + name + "-" + uid8 + suffix must fit in 63 chars.
	overhead := len("fn-") + len("-") + len(uid8) + len(suffix)
	if max := 63 - overhead; len(name) > max {
		if max < 0 {
			max = 0
		}
		name = name[:max]
	}
	return fmt.Sprintf("fn-%s-%s%s", name, uid8, suffix)
}

// functionServiceSelector matches exactly the pods specialized for this
// function at its current generation that have completed specialization
// (RFC-0002): the fission.io/served gate keeps relabeled-but-unspecialized
// pods out of the EndpointSlices, and the generation label keeps
// stale-generation pods out after a function update.
//
// FUNCTION_VERSION is added when fn carries it: belt-and-braces alongside
// FUNCTION_GENERATION, which already uniquely maps to the version (see
// versioning.VersionedFunction's invariant) -- adding it too keeps the
// Service/pod selector self-describing without changing what it matches.
func functionServiceSelector(fn *fv1.Function) map[string]string {
	sel := map[string]string{
		fv1.FUNCTION_UID:        string(fn.UID),
		fv1.FUNCTION_GENERATION: strconv.FormatInt(fn.Generation, 10),
		fv1.SERVED_LABEL:        fv1.SERVED_VALUE,
	}
	if v := fn.Labels[fv1.FUNCTION_VERSION]; v != "" {
		sel[fv1.FUNCTION_VERSION] = v
	}
	return sel
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

	labels := map[string]string{
		fv1.MANAGED_BY_LABEL:   fv1.MANAGED_BY_VALUE,
		fv1.EXECUTOR_TYPE:      string(fv1.ExecutorTypePoolmgr),
		fv1.FUNCTION_NAME:      fn.Name,
		fv1.FUNCTION_NAMESPACE: fn.Namespace,
		fv1.FUNCTION_UID:       string(fn.UID),
	}
	// Mirrored by the built-in EndpointSlice controller onto every slice
	// this Service owns, which is where fnKeyForSlice (endpointcache) reads
	// it back to key the router's per-version endpoint index entry.
	if v := fn.Labels[fv1.FUNCTION_VERSION]; v != "" {
		labels[fv1.FUNCTION_VERSION] = v
	}

	desired := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
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
// (read-mostly) ensure per function (per version -- see fnSvcEnsureKey).
const fnSvcEnsureDebounce = 30 * time.Second

// fnSvcEnsureKey is the gpm.fnSvcEnsured debounce key: the function UID plus
// its version label, so ensuring one version's Service has its own debounce
// window independent of any other version's. Without this, keying on UID
// alone meant a v2 ensure arriving inside v1's 30s debounce window was
// silently skipped -- starving v2's Service creation entirely as long as v1
// traffic kept re-triggering the shared UID entry. An unversioned Function
// (no FUNCTION_VERSION label) gets the bare "<uid>/" key, equivalent to
// today's UID-only behavior.
func fnSvcEnsureKey(fn *fv1.Function) string {
	return string(fn.UID) + "/" + fn.Labels[fv1.FUNCTION_VERSION]
}

// maybeEnsureFunctionService fires an async, debounced ensure of the
// function's headless Service. Called from both the cold-start path (first
// creation) and the warm RPC cache-hit path: the latter is the self-healing
// loop — a lost ensure (executor rolled mid-flight) leaves the function
// without slices, which routes all its traffic through the RPC path, which
// re-triggers the ensure here. Debounced per (function UID, version) --see
// fnSvcEnsureKey -- so steady-state traffic adds no API reads; skipped for
// OnceOnly functions, whose pods serve exactly one request and must never be
// admitted from slices.
func (gpm *GenericPoolManager) maybeEnsureFunctionService(fn *fv1.Function) {
	if !gpm.functionServicesEnabled || fn.Spec.OnceOnly {
		return
	}
	key := fnSvcEnsureKey(fn)
	if v, ok := gpm.fnSvcEnsured.Load(key); ok {
		if last, ok := v.(time.Time); ok && time.Since(last) < fnSvcEnsureDebounce {
			return
		}
	}
	// Optimistic stamp dedups concurrent triggers; the failure path below
	// removes it so the next request retries immediately.
	gpm.fnSvcEnsured.Store(key, time.Now())
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
		gpm.fnSvcEnsured.Delete(fnSvcEnsureKey(fn))
		metrics.RecordFunctionServiceEnsure(ctx, "error")
		gpm.logger.Error(err, "failed to ensure function service; warm-path endpoint discovery degrades to executor RPC for this function",
			"function", fn.Name, "namespace", fn.Namespace)
	}
}

// deleteFunctionService removes every headless Service the executor created
// for this function -- the unversioned one plus every per-version one
// (RFC-0025). Idempotent (no matching Services is success). Driven by the
// Function reconciler on delete — the owner reference covers same-namespace
// installs, this covers cross-namespace ones and keeps both paths symmetric.
//
// Lists by the labels actually stamped in ensureFunctionService
// (FUNCTION_UID + MANAGED_BY_LABEL) rather than deleting the single
// unversioned functionServiceName(fn): a versioned Function has one Service
// per published version, each with its own name, and deleting only the
// unversioned name orphaned every one of them.
func (gpm *GenericPoolManager) deleteFunctionService(ctx context.Context, fn *fv1.Function) error {
	ns := gpm.nsResolver.GetFunctionNS(fn.Namespace)
	selector := labels.SelectorFromSet(labels.Set{
		fv1.FUNCTION_UID:     string(fn.UID),
		fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE,
		fv1.EXECUTOR_TYPE:    string(fv1.ExecutorTypePoolmgr),
	}).String()
	svcs, err := gpm.kubernetesClient.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	// Fail-fast on the first delete error rather than aggregating: the
	// Function reconciler retries the whole delete on any error, which
	// re-Lists and re-attempts every remaining Service (including ones this
	// pass already deleted, now idempotently absent) -- no partial-progress
	// state to preserve across attempts, so there's nothing an aggregated
	// error would buy over the simpler early return.
	for i := range svcs.Items {
		name := svcs.Items[i].Name
		if err := gpm.kubernetesClient.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
