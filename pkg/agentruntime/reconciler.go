// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// reconciler.go implements AgentReconciler, which keeps AgentView in sync
// with Function CRDs. It mirrors pkg/mcp's FunctionToolReconciler shape:
// cache-backed client.Get, IsNotFound -> remove, Spec.Agent == nil -> remove,
// otherwise build the defaults-applied entry and Upsert it into the view.
//
// Follow-up (not in v1): no status condition is written back to the Function
// (unlike mcp's ToolExposed). Once the agent runtime has an observable
// signal worth surfacing (e.g. an AgentReady/AgentExposed condition), add it
// here the same way pkg/mcp/reconciler.go's controller.SetConditions calls
// do.
package agentruntime

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// AgentReconciler keeps AgentView in sync with Function CRDs. It runs on
// every replica (no leader election, mirroring pkg/mcp): each replica serves
// requests from its own view, and the work is idempotent map mutation so
// concurrent replicas do not conflict.
type AgentReconciler struct {
	logger logr.Logger
	client client.Client
	view   *AgentView
}

// NewAgentReconciler returns an AgentReconciler that maintains view from c.
func NewAgentReconciler(logger logr.Logger, c client.Client, view *AgentView) *AgentReconciler {
	return &AgentReconciler{logger: logger, client: c, view: view}
}

func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			r.view.Remove(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if fn.Spec.Agent == nil {
		r.view.Remove(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	entry := entryFromAgentConfig(fn.Namespace, fn.Name, fn.Spec.Agent)
	r.view.Upsert(entry)
	return ctrl.Result{}, nil
}
