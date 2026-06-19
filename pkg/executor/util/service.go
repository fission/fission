// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// CreateOrAdoptService ensures the desired Service exists in its namespace.
//
// If a Service with the same name already exists but is not managed by this
// executor instance — its EXECUTOR_INSTANCEID annotation doesn't match, or it
// lacks the RFC-0002 managed-by label (an orphan created before RFC-0002) — it
// is adopted in place by overwriting the managed metadata and the relevant spec
// fields so the EndpointSlice controller mirrors the managed-by label onto its
// slices and the router's label-filtered informer sees them. Otherwise the
// desired Service is created, tolerating a racing AlreadyExists by re-reading
// the winner.
//
// created reports whether the not-found/create path was taken, so the caller
// can emit its own create span event (the newdeploy and container managers use
// different event names). desired must be fully populated by the caller.
//
// Shared by the newdeploy and container managers, whose per-function Service
// adoption logic was otherwise byte-identical.
func CreateOrAdoptService(ctx context.Context, kubeClient kubernetes.Interface, logger logr.Logger, instanceID, namespace string, desired *apiv1.Service) (*apiv1.Service, bool, error) {
	svcName := desired.Name

	existingSvc, err := kubeClient.CoreV1().Services(namespace).Get(ctx, svcName, metav1.GetOptions{})
	if err == nil {
		// to adopt orphan service (the managed-by check upgrades Services
		// created before RFC-0002 so their slices become router-visible)
		if existingSvc.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != instanceID ||
			existingSvc.Labels[fv1.MANAGED_BY_LABEL] != fv1.MANAGED_BY_VALUE {
			existingSvc.Annotations = desired.Annotations
			existingSvc.Labels = desired.Labels
			existingSvc.OwnerReferences = desired.OwnerReferences
			existingSvc.Spec.Ports = desired.Spec.Ports
			existingSvc.Spec.Selector = desired.Spec.Selector
			existingSvc.Spec.Type = desired.Spec.Type
			existingSvc, err = kubeClient.CoreV1().Services(namespace).Update(ctx, existingSvc, metav1.UpdateOptions{})
			if err != nil {
				logger.Error(err, "error adopting service", "service", svcName, "ns", namespace)
				return nil, false, err
			}
		}
		return existingSvc, false, nil
	} else if k8serrors.IsNotFound(err) {
		svc, err := kubeClient.CoreV1().Services(namespace).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			if k8serrors.IsAlreadyExists(err) {
				svc, err = kubeClient.CoreV1().Services(namespace).Get(ctx, svcName, metav1.GetOptions{})
			}
			if err != nil {
				return nil, false, err
			}
		}
		return svc, true, nil
	}
	return nil, false, err
}
