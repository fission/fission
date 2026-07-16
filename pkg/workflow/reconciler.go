// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
)

// runResyncInterval re-drives Running runs even with no wake: the drift
// guard for lost wakes, and what re-arms a timer lost to the DLQ.
const runResyncInterval = 60 * time.Second

// WorkflowRunReconciler drives runs through the engine and mirrors the fold
// into status. All correctness lives in the CAS-protected log; status is a
// best-effort view.
type WorkflowRunReconciler struct {
	logger logr.Logger
	client client.Client
	engine *Engine
}

func (r *WorkflowRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &fv1.WorkflowRun{}
	if err := r.client.Get(ctx, req.NamespacedName, run); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil // stream GC is the phase-3 finalizer's job
		}
		return ctrl.Result{}, err
	}
	if run.Status.Phase.Terminal() {
		return ctrl.Result{}, nil
	}

	fetch := func(ctx context.Context) (*fv1.WorkflowSpec, error) {
		wf := &fv1.Workflow{}
		key := client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.WorkflowRef}
		if err := r.client.Get(ctx, key, wf); err != nil {
			return nil, fmt.Errorf("fetching workflow %q: %w", run.Spec.WorkflowRef, err)
		}
		return &wf.Spec, nil
	}

	s, err := r.engine.Reconcile(ctx, run, fetch)
	if err != nil {
		// A missing Workflow is GitOps ordering, not a hard failure: surface
		// it on the Accepted condition and retry on the resync cadence.
		controller.SetConditions(ctx, r.logger, r.client, run, metav1.Condition{
			Type: fv1.WorkflowRunConditionAccepted, Status: metav1.ConditionFalse,
			Reason: "EngineError", Message: err.Error(),
		})
		return ctrl.Result{RequeueAfter: runResyncInterval}, nil
	}

	r.writeStatus(ctx, run, s)

	if s.Terminal != "" {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: runResyncInterval}, nil
}

// writeStatus mirrors the fold into the run's status subresource
// (best-effort — the log is the truth).
func (r *WorkflowRunReconciler) writeStatus(ctx context.Context, run *fv1.WorkflowRun, s *RunState) {
	run.Status.ObservedGeneration = run.Generation
	run.Status.RecentEvents = s.Recent

	switch {
	case s.Terminal != "":
		run.Status.Phase = s.Terminal
		if run.Status.FinishedAt == nil {
			now := metav1.Now()
			run.Status.FinishedAt = &now
		}
		run.Status.ActiveStates = nil
		// The final output rides inline up to the spill threshold; larger
		// results stay in KV and OutputRef points there (a big result must
		// not turn the terminal status write into the failure).
		if len(s.Output) > 0 && len(s.Output) <= spillThreshold {
			run.Status.Output = &runtime.RawExtension{Raw: s.Output}
		}
		run.Status.OutputRef = s.OutputRef
	case s.Spec != nil:
		run.Status.Phase = fv1.WorkflowRunRunning
		if run.Status.StartedAt == nil {
			run.Status.StartedAt = &metav1.Time{Time: s.StartedAt}
		}
		if s.Current != "" {
			run.Status.ActiveStates = []string{s.Current}
		} else {
			run.Status.ActiveStates = nil
		}
	default:
		run.Status.Phase = fv1.WorkflowRunPending
	}

	if err := r.client.Status().Update(ctx, run); err != nil {
		r.logger.V(1).Info("run status update failed (reconverges next reconcile)", "run", run.Name, "error", err)
		return
	}
	controller.SetConditions(ctx, r.logger, r.client, run, metav1.Condition{
		Type: fv1.WorkflowRunConditionAccepted, Status: metav1.ConditionTrue,
		Reason: fv1.WorkflowRunReasonAccepted, Message: "a workflow controller is executing this run",
	})
}
