/*
Copyright 2026 The Fission Authors.

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

package util

import (
	"context"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// SetFunctionReady writes the FunctionConditionReady condition on the named
// Function CR using the status subresource. Shared by every executor type
// (poolmgr, newdeploy, container) so they all surface readiness on the same
// condition shape.
//
// Fast-path: the caller passes an in-memory fn; if its Status.Conditions
// already record the same Status/Reason/ObservedGeneration we want to
// write, we skip the apiserver call entirely. Only key transitions
// (cold-start success, transition False↔True, generation bump) reach the
// network.
//
// Best-effort: status I/O failures are logged at V(1) and never propagated
// back to the caller — the executor's primary job is to return a usable
// service URL, not to gate on status writes.
func SetFunctionReady(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, fn *fv1.Function, status metav1.ConditionStatus, reason, message string) {
	if fissionClient == nil {
		return
	}
	want := metav1.Condition{
		Type:               fv1.FunctionConditionReady,
		Status:             status,
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

// SetEnvironmentReady writes the EnvironmentConditionReady condition on the
// named Environment CR using the status subresource. Used by the poolmgr to
// signal "at least one runtime pod is ready in the pool"
// (Reason=PoolReady), which is a stronger readiness signal than the
// buildermgr's BuilderReady.
//
// Same fast-path semantics as SetFunctionReady — but we don't have an
// in-memory Environment from the caller, so the cheap pre-check is skipped
// and we always Get. The post-Get IsAt check still keeps the UpdateStatus
// rate down to the actual transitions.
func SetEnvironmentReady(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, namespace, name string, status metav1.ConditionStatus, reason, message string) {
	if fissionClient == nil {
		return
	}
	cur, err := fissionClient.CoreV1().Environments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		logger.V(1).Info("environment status: get failed", "name", name, "namespace", namespace, "error", err)
		return
	}
	want := metav1.Condition{
		Type:               fv1.EnvironmentConditionReady,
		Status:             status,
		ObservedGeneration: cur.Generation,
		Reason:             reason,
		Message:            message,
	}
	if conditions.IsAt(cur.Status.Conditions, want) {
		return
	}
	if !conditions.Set(&cur.Status.Conditions, want) {
		return
	}
	if _, err := fissionClient.CoreV1().Environments(namespace).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		logger.V(1).Info("environment status: update failed", "name", name, "namespace", namespace, "error", err)
	}
}
