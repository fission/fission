// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

const (
	// FinalizerName guards run deletion until the stream and KV scopes are
	// reclaimed — whether the delete came from kubectl, the TTL sweeper, or
	// namespace deletion. Workflows have the CR to hang cleanup on (the hole
	// RFC-0027 had to defer never opens).
	FinalizerName = "fission.io/workflow-gc"

	// WorkflowRefIndex is the field index the sweeper lists runs by.
	WorkflowRefIndex = "spec.workflowRef"

	sweepInterval = 10 * time.Minute
)

// CleanupRun reclaims a deleted run's statestore footprint: every event
// payload (Trim keeps only the stream-head marker — one tiny row, documented
// in the RFC) and the io/checkpoint KV keyspaces.
func (e *Engine) CleanupRun(ctx context.Context, namespace, name string, uid types.UID) error {
	stream := streamNameForUID(string(uid))
	head, err := e.el.Head(ctx, stream)
	if err != nil {
		return fmt.Errorf("reading head for cleanup: %w", err)
	}
	if head > 0 {
		if err := e.el.Trim(ctx, stream, head+1); err != nil {
			return fmt.Errorf("trimming stream: %w", err)
		}
	}
	for _, scope := range []statestore.Scope{ioScope(namespace, name), checkpointScope(namespace, name)} {
		if err := e.deleteKeyspace(ctx, scope); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) deleteKeyspace(ctx context.Context, scope statestore.Scope) error {
	token := ""
	for {
		page, err := e.kv.List(ctx, scope, "", statestore.Page{Token: token})
		if err != nil {
			return fmt.Errorf("listing %s keyspace: %w", scope.Keyspace, err)
		}
		for _, key := range page.Keys {
			if err := e.kv.Delete(ctx, scope, key, 0); err != nil {
				return fmt.Errorf("deleting %s/%s: %w", scope.Keyspace, key, err)
			}
		}
		if page.Next == "" {
			return nil
		}
		token = page.Next
	}
}

// RetentionSweeper deletes finished runs beyond their Workflow's
// HistoryRetention (count and age). Runs carrying the finalizer clean up on
// deletion; runs that predate it (upgrade edge) get best-effort direct
// cleanup first.
type RetentionSweeper struct {
	client client.Client
	engine *Engine
}

// Start implements manager.Runnable (started after cache sync).
func (rs *RetentionSweeper) Start(ctx context.Context) error {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			rs.sweep(ctx)
		}
	}
}

func (rs *RetentionSweeper) sweep(ctx context.Context) {
	logger := rs.engine.logger.WithName("retention")

	var wfs fv1.WorkflowList
	if err := rs.client.List(ctx, &wfs); err != nil {
		logger.Error(err, "listing workflows")
		return
	}
	for _, wf := range wfs.Items {
		if wf.Spec.HistoryRetention == nil {
			continue
		}
		var runs fv1.WorkflowRunList
		if err := rs.client.List(ctx, &runs, client.InNamespace(wf.Namespace),
			client.MatchingFields{WorkflowRefIndex: wf.Name}); err != nil {
			logger.Error(err, "listing runs", "workflow", wf.Name)
			continue
		}

		var finished []fv1.WorkflowRun
		for _, run := range runs.Items {
			if run.Status.Phase.Terminal() && run.DeletionTimestamp == nil {
				finished = append(finished, run)
			}
		}
		// Newest first; retention keeps the head of the list.
		slices.SortFunc(finished, func(a, b fv1.WorkflowRun) int {
			return finishedAt(b).Compare(finishedAt(a))
		})

		ret := wf.Spec.HistoryRetention
		for i, run := range finished {
			expired := ret.MaxAge != nil && time.Since(finishedAt(run)) > ret.MaxAge.Duration
			overCount := ret.MaxCount != nil && i >= int(*ret.MaxCount)
			if !expired && !overCount {
				continue
			}
			if !controllerutil.ContainsFinalizer(&run, FinalizerName) {
				// Pre-finalizer run (upgrade edge): reclaim directly, since
				// deletion won't trigger cleanup.
				if err := rs.engine.CleanupRun(ctx, run.Namespace, run.Name, run.UID); err != nil {
					logger.Error(err, "direct cleanup before delete", "run", run.Name)
					continue // retry next sweep rather than leak
				}
			}
			// The UID precondition stops a stale cached listing from deleting
			// a RECREATED (possibly Running) run under the same name.
			if err := rs.client.Delete(ctx, &run, client.Preconditions{UID: &run.UID}); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				logger.Error(err, "deleting expired run", "run", run.Name)
				continue
			}
			logger.Info("retention deleted finished run", "workflow", wf.Name, "run", run.Name)
		}
	}
}

func finishedAt(run fv1.WorkflowRun) time.Time {
	if run.Status.FinishedAt != nil {
		return run.Status.FinishedAt.Time
	}
	return run.CreationTimestamp.Time
}

// handleDeletion runs the finalizer protocol; returns true when the
// reconcile should stop (the run is going away).
func (r *WorkflowRunReconciler) handleDeletion(ctx context.Context, run *fv1.WorkflowRun) (bool, error) {
	if run.DeletionTimestamp == nil {
		if controllerutil.AddFinalizer(run, FinalizerName) {
			if err := r.client.Update(ctx, run); err != nil {
				return true, err
			}
		}
		return false, nil
	}
	if !controllerutil.ContainsFinalizer(run, FinalizerName) {
		return true, nil // nothing to do; someone else's finalizer or none
	}
	if err := r.engine.CleanupRun(ctx, run.Namespace, run.Name, run.UID); err != nil {
		return true, err // requeue via error; the CR stays until cleanup lands
	}
	controllerutil.RemoveFinalizer(run, FinalizerName)
	if err := r.client.Update(ctx, run); err != nil {
		return true, err
	}
	return true, nil
}
