// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package tenant implements the fission-bundle --tenantController subsystem: the
// lifecycle controller for multi-namespace tenancy (docs/multiple-namespace).
// It reconciles FissionTenant CRs (and Namespaces labelled fission.io/enabled)
// into the live resource-namespace set, provisions per-namespace RBAC and
// service accounts for the fetcher/builder (tearing them down on offboard via a
// finalizer), and reports readiness. Per-namespace HMAC auth-key provisioning is
// layered on in a later phase.
package tenant

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	corev1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/utils"
)

const (
	// EnabledLabel on a Namespace opts it in as a Fission tenant; the controller
	// materializes a labelled Namespace into a FissionTenant CR (one-way
	// ignition — removing the label never deletes the CR; see prd.md §6.2).
	EnabledLabel = "fission.io/enabled"

	// managedByAnnotation records how a FissionTenant came to exist: "label"
	// (materialized from EnabledLabel) vs a user/helm-authored CR. Only
	// label-managed CRs carry it. The key is the shared fission.io/managed-by
	// constant; the values here are provenance-specific (label/helm/user).
	managedByAnnotation = fv1.MANAGED_BY_LABEL
	managedByLabel      = "label"

	// tenantFinalizer guards teardown of the per-namespace RBAC the controller
	// provisions, so it is removed before the FissionTenant object disappears.
	tenantFinalizer = "fission.io/tenant-cleanup"

	// Condition reasons.
	ReasonOnboarded            = "Onboarded"
	ReasonNamespaceNotFound    = "NamespaceNotFound"
	ReasonRolesApplied         = "RolesApplied"
	ReasonServiceAccountsReady = "ServiceAccountsReady"
	ReasonProvisioningFailed   = "ProvisioningFailed"
	ReasonKeysDerived          = "KeysDerived"
)

// TenantReconciler reconciles FissionTenant CRs into the live resolver set and
// reports a Ready condition. It runs on the leader-elected --tenantController
// Manager (it writes status). The resolver drive is additive over the
// env-seeded set (utils.GetNamespaces) so an empty tenant list never wipes the
// env default; the env→CR source flip is a later phase.
type TenantReconciler struct {
	logger   logr.Logger
	client   client.Client
	resolver *utils.NamespaceResolver
	// master is the internal-auth master secret used to derive per-namespace
	// keys. Empty (internalAuth disabled) skips auth-key provisioning.
	master []byte
	// releaseNamespace is the install namespace where the executor/buildermgr SAs
	// live; it is the subject namespace for the workload RoleBindings provisioned
	// into each tenant namespace. Empty skips that binding (static Roles apply).
	releaseNamespace string
}

func (r *TenantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ft := &fv1.FissionTenant{}
	if err := r.client.Get(ctx, req.NamespacedName, ft); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: re-derive the live set without it.
			return ctrl.Result{}, r.syncResolver(ctx)
		}
		return ctrl.Result{}, err
	}

	// Offboard: tear down the per-namespace RBAC the controller provisioned, then
	// release the finalizer so the object can be removed. User Functions are left
	// in place (disabling onboarding is not deleting content).
	if !ft.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(ft, tenantFinalizer) {
			if err := DeleteNamespaceRBAC(ctx, r.client, ft.Spec.Namespace); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(ft, tenantFinalizer)
			if err := r.client.Update(ctx, ft); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, r.syncResolver(ctx)
	}
	// Add the cleanup finalizer before provisioning, so teardown always runs.
	if controllerutil.AddFinalizer(ft, tenantFinalizer) {
		if err := r.client.Update(ctx, ft); err != nil {
			return ctrl.Result{}, err
		}
	}
	ft.Status.ObservedGeneration = ft.Generation

	target := &corev1.Namespace{}
	err := r.client.Get(ctx, types.NamespacedName{Name: ft.Spec.Namespace}, target)
	switch {
	case apierrors.IsNotFound(err):
		controller.SetConditions(ctx, r.logger, r.client, ft, metav1.Condition{
			Type:    fv1.FissionTenantConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  ReasonNamespaceNotFound,
			Message: fmt.Sprintf("namespace %q does not exist", ft.Spec.Namespace),
		})
		// Still sync: the tenant is declared even if its namespace is absent, but
		// a missing namespace contributes nothing to watch.
		return ctrl.Result{}, r.syncResolver(ctx)
	case err != nil:
		return ctrl.Result{}, err
	}

	// Namespace exists: provision the per-namespace RBAC + service accounts the
	// fetcher/builder need (idempotent; the runtime equivalent of the chart's
	// _function-access-role.tpl). Ready gates on it. The provisioned objects are
	// owned by this FissionTenant (a GC backstop in case the finalizer is bypassed
	// by a force-delete).
	owner := metav1.OwnerReference{
		APIVersion: "fission.io/v1",
		Kind:       "FissionTenant",
		Name:       ft.Name,
		UID:        ft.UID,
	}
	if err := EnsureNamespaceRBAC(ctx, r.client, ft.Spec.Namespace, r.releaseNamespace, owner); err != nil {
		controller.SetConditions(ctx, r.logger, r.client, ft, metav1.Condition{
			Type:    fv1.FissionTenantConditionRBACProvisioned,
			Status:  metav1.ConditionFalse,
			Reason:  ReasonProvisioningFailed,
			Message: err.Error(),
		})
		return ctrl.Result{}, fmt.Errorf("provisioning RBAC for tenant %q: %w", ft.Spec.Namespace, err)
	}
	// Provision the per-namespace derived-key auth Secret (no-op when internalAuth
	// is disabled / master is empty).
	if err := EnsureNamespaceAuthSecret(ctx, r.client, r.master, ft.Spec.Namespace); err != nil {
		controller.SetConditions(ctx, r.logger, r.client, ft, metav1.Condition{
			Type:    fv1.FissionTenantConditionAuthKeyProvisioned,
			Status:  metav1.ConditionFalse,
			Reason:  ReasonProvisioningFailed,
			Message: err.Error(),
		})
		return ctrl.Result{}, fmt.Errorf("provisioning auth secret for tenant %q: %w", ft.Spec.Namespace, err)
	}

	conds := []metav1.Condition{
		{
			Type:    fv1.FissionTenantConditionRBACProvisioned,
			Status:  metav1.ConditionTrue,
			Reason:  ReasonRolesApplied,
			Message: "per-namespace RBAC provisioned",
		},
		{
			Type:    fv1.FissionTenantConditionServiceAccountsReady,
			Status:  metav1.ConditionTrue,
			Reason:  ReasonServiceAccountsReady,
			Message: "fetcher and builder service accounts present",
		},
	}
	if len(r.master) > 0 {
		conds = append(conds, metav1.Condition{
			Type:    fv1.FissionTenantConditionAuthKeyProvisioned,
			Status:  metav1.ConditionTrue,
			Reason:  ReasonKeysDerived,
			Message: "per-namespace internal-auth keys provisioned",
		})
	}
	conds = append(conds, metav1.Condition{
		Type:    fv1.FissionTenantConditionReady,
		Status:  metav1.ConditionTrue,
		Reason:  ReasonOnboarded,
		Message: fmt.Sprintf("namespace %q is onboarded", ft.Spec.Namespace),
	})
	controller.SetConditions(ctx, r.logger, r.client, ft, conds...)
	return ctrl.Result{}, r.syncResolver(ctx)
}

// syncResolver sets the live resource-namespace set to the union of the
// env-seeded namespaces and every FissionTenant's spec.namespace. Listing on each
// reconcile is cheap (tenants number in the tens) and makes deletes
// self-correcting. It shares SyncResolverFromTenants with the data-plane
// resolver-sync so the controller and every other process compute the set
// identically.
func (r *TenantReconciler) syncResolver(ctx context.Context) error {
	return SyncResolverFromTenants(ctx, r.client, r.resolver)
}

// namespaceToRequests maps a Namespace event to reconcile requests for every
// FissionTenant whose spec.namespace matches, so a tenant's Ready condition and
// the resolver set re-converge when its namespace is created or deleted out of
// band (the Ready condition is a function of namespace existence, external state
// the FissionTenant watch alone cannot observe).
func (r *TenantReconciler) namespaceToRequests(ctx context.Context, obj client.Object) []ctrl.Request {
	list := &fv1.FissionTenantList{}
	if err := r.client.List(ctx, list); err != nil {
		// This mapper is the only path that re-converges a tenant's Ready condition
		// on namespace create/delete; a persistent List failure silently strands
		// tenants at NamespaceNotFound, so surface it at Error (not V(1)).
		r.logger.Error(err, "namespace-to-tenant mapping: list failed", "namespace", obj.GetName())
		return nil
	}
	var reqs []ctrl.Request
	for i := range list.Items {
		if list.Items[i].Spec.Namespace == obj.GetName() {
			reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
	}
	return reqs
}

// NamespaceReconciler materializes a Namespace labelled fission.io/enabled=true
// into a FissionTenant CR. It is the "label is sugar" ignition path: it only ever
// creates, never deletes — removing the label leaves the CR in place (operators
// disable a tenant explicitly via `fission tenant disable`).
type NamespaceReconciler struct {
	logger logr.Logger
	client client.Client
}

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	target := &corev1.Namespace{}
	if err := r.client.Get(ctx, req.NamespacedName, target); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if target.Labels[EnabledLabel] != "true" {
		return ctrl.Result{}, nil
	}

	// Already onboarded by some FissionTenant (label-born or user-authored under
	// any name)? Then there is nothing to materialize.
	list := &fv1.FissionTenantList{}
	if err := r.client.List(ctx, list); err != nil {
		return ctrl.Result{}, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.Namespace == target.Name {
			return ctrl.Result{}, nil
		}
	}

	ft := &fv1.FissionTenant{
		ObjectMeta: metav1.ObjectMeta{
			Name:        target.Name,
			Annotations: map[string]string{managedByAnnotation: managedByLabel},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "Namespace",
				Name:       target.Name,
				UID:        target.UID,
			}},
		},
		Spec: fv1.FissionTenantSpec{Namespace: target.Name},
	}
	if err := r.client.Create(ctx, ft); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	r.logger.Info("materialized FissionTenant from namespace label", "namespace", target.Name)
	return ctrl.Result{}, nil
}
