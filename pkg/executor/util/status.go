// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// SetFunctionReady writes FunctionConditionReady=True with the given reason
// on the named Function CR via the status subresource. Shared by every
// executor type (poolmgr, newdeploy, container).
//
// The signature deliberately omits a ConditionStatus argument: executors
// only write the *success* transition. getFuncSvc and friends run on the
// cold-start hot path and may see many transient failures in quick
// succession (image pull retries, specialize timeouts, HPA conflicts) —
// flipping Ready=False on each would generate condition flapping that
// is more noise than signal. Transient failure observability is covered
// by `metrics.ColdStartsError` and structured logs.
//
// Fast-path: the caller passes an in-memory fn; if its Status.Conditions
// already record Ready=True with the same Reason/ObservedGeneration we
// want, we skip the apiserver call entirely. Only the first cold-start
// success per (function, generation) tuple reaches the network.
//
// Best-effort: status I/O failures are logged at V(1) and never propagated
// back to the caller — the executor's primary job is to return a usable
// service URL, not to gate on status writes.
func SetFunctionReady(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, fn *fv1.Function, reason, message string) {
	if fissionClient == nil {
		return
	}
	want := metav1.Condition{
		Type:               fv1.FunctionConditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: fn.Generation,
		Reason:             reason,
		Message:            message,
	}
	if conditions.IsAt(fn.Status.Conditions, want) {
		return
	}
	cur, err := fissionClient.CoreV1().Functions(fn.Namespace).Get(ctx, fn.Name, metav1.GetOptions{})
	if err != nil {
		logger.V(1).Info("function status: get failed", "name", fn.Name, "namespace", fn.Namespace, "error", err)
		return
	}
	want.ObservedGeneration = cur.Generation
	if conditions.IsAt(cur.Status.Conditions, want) {
		return
	}
	if !conditions.Set(&cur.Status.Conditions, want) {
		return
	}
	if _, err := fissionClient.CoreV1().Functions(fn.Namespace).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		logger.V(1).Info("function status: update failed", "name", fn.Name, "namespace", fn.Namespace, "error", err)
	}
}

// No SetEnvironmentReady helper is exported by this package. Writing the
// EnvironmentConditionReady condition would bump env.ResourceVersion via
// the status subresource, and the buildermgr's builder service hostname
// is currently "<env.Name>-<env.ResourceVersion>" (see
// pkg/buildermgr/common.go.buildPackage). The RV bump from a status
// write therefore breaks in-flight source-archive builds with DNS
// lookup failures. Until the builder service name is decoupled from
// env.ResourceVersion, no executor backend writes Environment status.
