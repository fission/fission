// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/executor/fscache"
)

// functionManager and envFunctionUpdater are the subsets of *NewDeploy the
// reconcilers drive — interfaces so the reconcile routing and the
// environment-image-change gate are unit-testable with fakes.
type functionManager interface {
	createFunction(context.Context, *fv1.Function) (*fscache.FuncSvc, error)
	updateFunction(context.Context, *fv1.Function, *fv1.Function) error
	deleteFunction(context.Context, *fv1.Function) error
}

type envFunctionUpdater interface {
	// updateEnvFunctionDeployments recreates the deployments of every function
	// that uses env (called when env's runtime image changes).
	updateEnvFunctionDeployments(context.Context, *fv1.Environment)
}

// functionReconciler manages newdeploy-backed function deployments. Like the
// container reconciler it replaces the Function informer handlers, and because
// updateFunction is diff-based it keeps the last-reconciled Function per key to
// supply the "old" object. See the container package's reconciler for the
// rationale. createFunction/deleteFunction self-filter to ExecutorTypeNewdeploy,
// so calling them for another type is a no-op; the reconciler still only caches
// newdeploy functions.
type functionReconciler struct {
	logger         logr.Logger
	client         client.Client
	deploy         functionManager
	lastReconciled sync.Map // client.ObjectKey -> *fv1.Function
}

func (r *functionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			if old, ok := r.lastReconciled.LoadAndDelete(req.NamespacedName); ok {
				if err := r.deploy.deleteFunction(ctx, old.(*fv1.Function)); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	old, seen := r.lastReconciled.Load(req.NamespacedName)
	if !seen {
		if !isNewdeployType(fn) {
			return ctrl.Result{}, nil
		}
		if _, err := r.deploy.createFunction(ctx, fn); err != nil {
			return ctrl.Result{}, err
		}
		r.lastReconciled.Store(req.NamespacedName, fn.DeepCopy())
		return ctrl.Result{}, nil
	}

	if err := r.deploy.updateFunction(ctx, old.(*fv1.Function), fn); err != nil {
		return ctrl.Result{}, err
	}
	if isNewdeployType(fn) {
		r.lastReconciled.Store(req.NamespacedName, fn.DeepCopy())
	} else {
		r.lastReconciled.Delete(req.NamespacedName)
	}
	return ctrl.Result{}, nil
}

func isNewdeployType(fn *fv1.Function) bool {
	return fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeNewdeploy
}

// environmentReconciler recreates the deployments of an environment's functions
// when the environment's runtime image changes — the only environment attribute
// that requires it today, matching the old EnvEventHandlers. It keeps the
// last-reconciled Environment per key to detect the image change (a reconciler
// has no "old" object), and does nothing on first sight (the old Add was a no-op).
type environmentReconciler struct {
	logger         logr.Logger
	client         client.Client
	deploy         envFunctionUpdater
	lastReconciled sync.Map // client.ObjectKey -> *fv1.Environment
}

func (r *environmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	env := &fv1.Environment{}
	if err := r.client.Get(ctx, req.NamespacedName, env); err != nil {
		if apierrors.IsNotFound(err) {
			r.lastReconciled.Delete(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	old, seen := r.lastReconciled.Load(req.NamespacedName)
	r.lastReconciled.Store(req.NamespacedName, env.DeepCopy())
	if !seen || old.(*fv1.Environment).Spec.Runtime.Image == env.Spec.Runtime.Image {
		return ctrl.Result{}, nil
	}

	r.logger.V(1).Info("environment runtime image changed; updating its functions",
		"environment", env.Name, "namespace", env.Namespace)
	r.deploy.updateEnvFunctionDeployments(ctx, env)
	return ctrl.Result{}, nil
}

// RegisterReconcilers registers the newdeploy Function + Environment reconcilers
// on the executor Manager.
func (deploy *NewDeploy) RegisterReconcilers(mgr ctrl.Manager) error {
	fnR := &functionReconciler{
		logger: deploy.logger.WithName("function_reconciler"),
		client: mgr.GetClient(),
		deploy: deploy,
	}
	if err := controller.Register(mgr, &fv1.Function{}, fnR, "newdeploy-function"); err != nil {
		return err
	}
	envR := &environmentReconciler{
		logger: deploy.logger.WithName("environment_reconciler"),
		client: mgr.GetClient(),
		deploy: deploy,
	}
	return controller.Register(mgr, &fv1.Environment{}, envR, "newdeploy-environment")
}
