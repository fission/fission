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

// workflowRefGrace bounds how long a run waits for its Workflow to appear
// (GitOps applies in arbitrary order, but not forever).
const workflowRefGrace = 10 * time.Minute

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
			return ctrl.Result{}, nil // the finalizer already reclaimed the stream/KV
		}
		return ctrl.Result{}, err
	}
	// The deletion/finalizer protocol sits ABOVE the terminal fast-exit:
	// terminal runs are exactly the ones the retention sweeper deletes.
	if done, err := r.handleDeletion(ctx, run); done {
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
		// A missing Workflow is GitOps ordering, not a hard failure — but
		// the grace is not infinite: a run whose Workflow never appears is
		// terminally failed so it does not consume a reconcile every minute
		// for the lifetime of the cluster.
		if apierrors.IsNotFound(err) && time.Since(run.CreationTimestamp.Time) > workflowRefGrace {
			if failErr := r.engine.FailUnstartable(ctx, run,
				fmt.Sprintf("workflow %q did not appear within %s of run creation", run.Spec.WorkflowRef, workflowRefGrace)); failErr == nil {
				return ctrl.Result{Requeue: true}, nil // fold the terminal event into status
			}
		}
		controller.SetConditions(ctx, r.logger, r.client, run, metav1.Condition{
			Type: fv1.WorkflowRunConditionAccepted, Status: metav1.ConditionFalse,
			Reason: "EngineError", Message: err.Error(),
		})
		return ctrl.Result{RequeueAfter: runResyncInterval}, nil
	}

	if err := r.writeStatus(ctx, run, s); err != nil {
		// A lost TERMINAL status write has no future wake to heal it (the
		// invoker and timers are done, and terminal runs fast-exit), so it
		// must requeue — otherwise the run displays Running forever.
		r.logger.Error(err, "run status update failed", "run", run.Name, "terminal", s.Terminal != "")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if s.Terminal != "" {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: runResyncInterval}, nil
}

// writeStatus mirrors the fold into the run's status subresource. The log is
// the truth, but the returned error matters: a lost terminal write has no
// future wake to heal it, so the caller requeues on failure.
func (r *WorkflowRunReconciler) writeStatus(ctx context.Context, run *fv1.WorkflowRun, s *RunState) error {
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
		// Failure classification so kubectl answers "why" (bounded; full
		// detail in the history).
		run.Status.ErrorType = s.ErrorType
		if cause := string(s.Cause); len(cause) > 0 {
			if len(cause) > 1024 {
				cause = cause[:1021] + "..."
			}
			run.Status.Cause = cause
		}
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
		return err
	}
	controller.SetConditions(ctx, r.logger, r.client, run, metav1.Condition{
		Type: fv1.WorkflowRunConditionAccepted, Status: metav1.ConditionTrue,
		Reason: fv1.WorkflowRunReasonAccepted, Message: "a workflow controller is executing this run",
	})
	return nil
}
