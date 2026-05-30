// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cms

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// ConfigMapReconciler and SecretReconciler replace the executor's ConfigMap +
// Secret informer event handlers. When a referenced ConfigMap/Secret's content
// changes, every Function that mounts it has its pods recycled (RefreshFuncPods)
// so the new data takes effect. They are registered with contentChangedPredicate
// so only a real content change triggers a reconcile — matching the old handlers,
// whose Add/Delete were no-ops and whose Update acted only on a ResourceVersion
// change.

// ConfigMapReconciler recycles pods of functions that reference a changed ConfigMap.
type ConfigMapReconciler struct {
	logger        logr.Logger
	client        client.Client
	fissionClient versioned.Interface
	types         map[fv1.ExecutorType]executortype.ExecutorType
}

func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cm := &apiv1.ConfigMap{}
	if err := r.client.Get(ctx, req.NamespacedName, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	funcs, err := getConfigmapRelatedFuncs(ctx, &cm.ObjectMeta, r.fissionClient)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get functions related to configmap %s/%s: %w", cm.Namespace, cm.Name, err)
	}
	if len(funcs) == 0 {
		return ctrl.Result{}, nil
	}
	r.logger.V(1).Info("configmap changed", "configmap_name", cm.Name, "configmap_namespace", cm.Namespace)
	refreshPods(ctx, r.logger, funcs, r.types)
	return ctrl.Result{}, nil
}

// SecretReconciler recycles pods of functions that reference a changed Secret.
type SecretReconciler struct {
	logger        logr.Logger
	client        client.Client
	fissionClient versioned.Interface
	types         map[fv1.ExecutorType]executortype.ExecutorType
}

func (r *SecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	s := &apiv1.Secret{}
	if err := r.client.Get(ctx, req.NamespacedName, s); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	funcs, err := getSecretRelatedFuncs(ctx, r.logger, &s.ObjectMeta, r.fissionClient)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get functions related to secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	if len(funcs) == 0 {
		return ctrl.Result{}, nil
	}
	r.logger.V(1).Info("secret changed", "secret_name", s.Name, "secret_namespace", s.Namespace)
	refreshPods(ctx, r.logger, funcs, r.types)
	return ctrl.Result{}, nil
}

// RegisterReconcilers wires the ConfigMap + Secret reconcilers onto the executor
// Manager. They watch through the Manager's (namespace-scoped) cache and, like
// the rest of the executor's mutating controllers, run on the elected leader
// only.
func RegisterReconcilers(mgr ctrl.Manager, logger logr.Logger, fissionClient versioned.Interface,
	types map[fv1.ExecutorType]executortype.ExecutorType) error {
	cmReconciler := &ConfigMapReconciler{
		logger:        logger.WithName("configmap_reconciler"),
		client:        mgr.GetClient(),
		fissionClient: fissionClient,
		types:         types,
	}
	if err := controller.RegisterWithPredicates(mgr, &apiv1.ConfigMap{}, cmReconciler, "executor-configmap", 0, contentChangedPredicate()); err != nil {
		return fmt.Errorf("error registering configmap reconciler: %w", err)
	}
	secretReconciler := &SecretReconciler{
		logger:        logger.WithName("secret_reconciler"),
		client:        mgr.GetClient(),
		fissionClient: fissionClient,
		types:         types,
	}
	if err := controller.RegisterWithPredicates(mgr, &apiv1.Secret{}, secretReconciler, "executor-secret", 0, contentChangedPredicate()); err != nil {
		return fmt.Errorf("error registering secret reconciler: %w", err)
	}
	return nil
}

// contentChangedPredicate enqueues a reconcile only on an Update whose
// ResourceVersion changed. Creates and Deletes are dropped, reproducing the old
// informer handlers (Add/Delete were no-ops; Update acted only on a real
// content change). This also avoids a refresh storm from the initial list of
// every ConfigMap/Secret in the watched namespaces on startup.
func contentChangedPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
		},
	}
}

func refreshPods(ctx context.Context, logger logr.Logger, funcs []fv1.Function, types map[fv1.ExecutorType]executortype.ExecutorType) {
	for _, f := range funcs {
		var err error

		et, exists := types[f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType]
		if exists {
			err = et.RefreshFuncPods(ctx, logger, f)
		} else {
			err = fmt.Errorf("unknown executor type '%s'", f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)
		}

		if err != nil {
			logger.Error(err, "Failed to recycle pods for function after configmap/secret changed", "function", f)
		}
	}
}
