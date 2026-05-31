// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	kedaClient "github.com/kedacore/keda/v2/pkg/generated/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// scalerReconciler keeps the KEDA objects (ScaledObject, Deployment,
// TriggerAuthentication) that back a keda-kind MessageQueueTrigger in sync with
// the MessageQueueTrigger CRDs. It replaces the previous informer +
// AddFunc/UpdateFunc handler: controller-runtime delivers add/update/delete
// through its own rate-limited workqueue, and the GenerationChangedPredicate (in
// controller.Register) drops the status-only updates the old handler never saw
// either.
//
// Reads go through the Manager's cache-backed client. A last-seen cache keyed by
// NamespacedName supplies the "old" object the informer's UpdateFunc used to
// receive, so the reconciler can both route create-vs-update and detect the
// keda<->fission kind transition exactly as the handler did.
//
// Deletion of a MessageQueueTrigger is NOT handled here, matching the original
// handler which registered no DeleteFunc: the KEDA objects carry OwnerReferences
// to the MQT, so Kubernetes garbage-collects them when the MQT is deleted. On a
// NotFound the reconciler only forgets its last-seen entry.
type scalerReconciler struct {
	logger     logr.Logger
	client     client.Client
	kedaClient kedaClient.Interface
	kubeClient kubernetes.Interface
	routerURL  string

	// mu guards lastSeen, which records the most recently reconciled spec per
	// trigger so a subsequent reconcile can compare against it (create-vs-update
	// and kind-transition detection). controller-runtime serializes reconciles
	// for the same key, but different keys reconcile in parallel, so the map
	// itself still needs a lock.
	mu       sync.Mutex
	lastSeen map[types.NamespacedName]*fv1.MessageQueueTrigger
}

// newScalerReconciler builds the reconciler. client is the Manager's
// cache-backed client; kedaClient/kubeClient drive the actual KEDA object
// create/update side effects; routerURL is the connector deployments'
// HTTP_ENDPOINT target.
func newScalerReconciler(logger logr.Logger, client client.Client, kedaClient kedaClient.Interface, kubeClient kubernetes.Interface, routerURL string) *scalerReconciler {
	return &scalerReconciler{
		logger:     logger.WithName("mqt_keda_scaler_reconciler"),
		client:     client,
		kedaClient: kedaClient,
		kubeClient: kubeClient,
		routerURL:  routerURL,
		lastSeen:   make(map[types.NamespacedName]*fv1.MessageQueueTrigger),
	}
}

func (r *scalerReconciler) getLastSeen(key types.NamespacedName) *fv1.MessageQueueTrigger {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSeen[key]
}

func (r *scalerReconciler) setLastSeen(key types.NamespacedName, mqt *fv1.MessageQueueTrigger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSeen[key] = mqt
}

func (r *scalerReconciler) forgetLastSeen(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.lastSeen, key)
}

func (r *scalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	mqt := &fv1.MessageQueueTrigger{}
	if err := r.client.Get(ctx, req.NamespacedName, mqt); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: the KEDA objects are garbage-collected via their
			// OwnerReferences to the MQT (the original handler had no
			// DeleteFunc). Just drop the last-seen entry.
			r.forgetLastSeen(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	old := r.getLastSeen(req.NamespacedName)

	// Mirror UpdateFunc's kind-transition handling. The old object is the
	// previously reconciled spec.
	if old != nil {
		mqtkindKedaToFission := old.Spec.MqtKind == MqtKindKeda && mqt.Spec.MqtKind == MqtKindFission
		mqtkindFissionToKeda := old.Spec.MqtKind == MqtKindFission && mqt.Spec.MqtKind == MqtKindKeda

		// keda -> fission: tear down the KEDA objects created earlier.
		if mqtkindKedaToFission {
			r.logger.V(1).Info("Mqtkind updated to fission from keda, cleanup keda objects", "mqt", mqt.ObjectMeta, "mqt.Spec", mqt.Spec)
			cleanupKedaObjects(ctx, r.logger, r.kedaClient, r.kubeClient, old)
			r.setLastSeen(req.NamespacedName, mqt.DeepCopy())
			return ctrl.Result{}, nil
		}

		// fission -> keda: create the KEDA objects now.
		if mqtkindFissionToKeda {
			r.logger.V(1).Info("Mqtkind changed to keda from fission, create keda objects", "mqt", mqt.ObjectMeta, "mqt.Spec", mqt.Spec)
			createKedaObjects(ctx, r.logger, r.kedaClient, r.kubeClient, mqt, r.routerURL)
			r.setLastSeen(req.NamespacedName, mqt.DeepCopy())
			return ctrl.Result{}, nil
		}
	}

	// Non-keda triggers are handled by the main mqt manager, not here.
	if mqt.Spec.MqtKind == MqtKindFission {
		r.setLastSeen(req.NamespacedName, mqt.DeepCopy())
		return ctrl.Result{}, nil
	}

	// First sight of a keda-kind trigger: create the KEDA objects (AddFunc).
	if old == nil {
		r.logger.V(1).Info("Create deployment for Scaler Object", "mqt", mqt.ObjectMeta, "mqt.Spec", mqt.Spec)
		createKedaObjects(ctx, r.logger, r.kedaClient, r.kubeClient, mqt, r.routerURL)
		r.setLastSeen(req.NamespacedName, mqt.DeepCopy())
		return ctrl.Result{}, nil
	}

	// Subsequent reconcile of a keda-kind trigger: diff against the last-seen
	// spec and update only the changed pieces (UpdateFunc's same-kind branch).
	// checkAndUpdateTriggerFields mutates a copy of the old spec in place, so
	// work on a copy and keep it as the new last-seen on success.
	merged := old.DeepCopy()
	updated := checkAndUpdateTriggerFields(merged, mqt)
	if !updated {
		r.logger.Info("Trigger unchanged, no changes found in trigger fields", "trigger_name", mqt.Name)
		r.setLastSeen(req.NamespacedName, mqt.DeepCopy())
		return ctrl.Result{}, nil
	}

	authenticationRef := ""
	if len(mqt.Spec.Secret) > 0 && mqt.Spec.Secret != old.Spec.Secret {
		authenticationRef = authTriggerName(merged.Name)
		if err := updateAuthTrigger(ctx, r.kedaClient, merged, authenticationRef, r.kubeClient); err != nil {
			r.logger.Error(err, "Failed to update Authentication Trigger")
			return ctrl.Result{}, err
		}
	}

	if err := updateDeployment(ctx, r.logger, merged, r.routerURL, r.kubeClient); err != nil {
		r.logger.Error(err, "Failed to Update Deployment")
		return ctrl.Result{}, err
	}

	if err := updateScaledObject(ctx, r.kedaClient, merged, authenticationRef); err != nil {
		r.logger.Error(err, "Failed to Update ScaledObject")
		return ctrl.Result{}, err
	}

	r.setLastSeen(req.NamespacedName, merged)
	return ctrl.Result{}, nil
}
