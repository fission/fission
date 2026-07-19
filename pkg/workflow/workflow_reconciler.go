// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
)

// WorkflowReconciler mirrors graph validation onto Workflow status. The
// admission webhook already rejects invalid graphs; this condition covers
// writes that bypassed it (webhook disabled, direct etcd restores) and gives
// GitOps a queryable signal.
type WorkflowReconciler struct {
	logger logr.Logger
	client client.Client
}

func (r *WorkflowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	wf := &fv1.Workflow{}
	if err := r.client.Get(ctx, req.NamespacedName, wf); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	cond := metav1.Condition{
		Type: fv1.WorkflowConditionValidated, Status: metav1.ConditionTrue,
		Reason: fv1.WorkflowReasonGraphValid, Message: "the state machine graph is valid",
	}
	if err := wf.Spec.Validate(); err != nil {
		cond.Status = metav1.ConditionFalse
		cond.Reason = fv1.WorkflowReasonGraphInvalid
		cond.Message = err.Error()
	}
	controller.SetConditions(ctx, r.logger, r.client, wf, cond)
	return ctrl.Result{}, nil
}
