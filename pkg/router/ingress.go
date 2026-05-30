// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"maps"
	"os"
	"reflect"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/util"
)

var podNamespace string

func init() {
	podNamespace = os.Getenv("POD_NAMESPACE")
	if podNamespace == "" {
		podNamespace = "fission"
	}
}

// reconcileIngress brings the Ingress for a trigger to its desired state
// (level-based, called from the HTTPTrigger reconciler): create it when
// Spec.CreateIngress is set and it is missing, update it when its spec drifts,
// and delete it when CreateIngress is unset. The Ingress lives in podNamespace
// and is named after the trigger.
func reconcileIngress(ctx context.Context, logger logr.Logger, trigger *fv1.HTTPTrigger, kubeClient kubernetes.Interface) {
	if !trigger.Spec.CreateIngress {
		// Ensure no Ingress lingers (e.g. CreateIngress was toggled off).
		deleteIngressByName(ctx, logger, trigger.Name, kubeClient)
		return
	}

	desired := util.GetIngressSpec(podNamespace, trigger)
	existing, err := kubeClient.NetworkingV1().Ingresses(podNamespace).Get(ctx, trigger.Name, v1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		_, err := kubeClient.NetworkingV1().Ingresses(podNamespace).Create(ctx, desired, v1.CreateOptions{})
		if err != nil && !k8serrors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create ingress", "trigger", trigger.Name)
			return
		}
		logger.V(1).Info("created ingress successfully for trigger", "trigger", trigger.Name)
		return
	}
	if err != nil {
		logger.Error(err, "failed to get ingress when reconciling trigger", "trigger", trigger.Name)
		return
	}

	changes := false
	if !reflect.DeepEqual(existing.Annotations, desired.Annotations) {
		if existing.Annotations == nil || desired.Annotations == nil {
			existing.Annotations = desired.Annotations
		} else {
			maps.Copy(existing.Annotations, desired.Annotations)
		}
		changes = true
	}
	if !reflect.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		changes = true
	}
	if changes {
		if _, err := kubeClient.NetworkingV1().Ingresses(podNamespace).Update(ctx, existing, v1.UpdateOptions{}); err != nil {
			logger.Error(err, "failed to update ingress for trigger", "trigger", trigger.Name)
			return
		}
		logger.V(1).Info("updated ingress successfully for trigger", "trigger", trigger.Name)
	}
}

// deleteIngressByName removes the Ingress with the given name from podNamespace.
// Idempotent: a missing Ingress is not an error, so this is safe to call for a
// trigger that never had CreateIngress set (e.g. on the reconciler's delete
// path, where the trigger object is already gone).
func deleteIngressByName(ctx context.Context, logger logr.Logger, name string, kubeClient kubernetes.Interface) {
	err := kubeClient.NetworkingV1().Ingresses(podNamespace).Delete(ctx, name, v1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		logger.Error(err, "failed to delete ingress", "ingress", name)
	}
}
