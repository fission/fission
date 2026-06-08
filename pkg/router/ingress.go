// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"maps"
	"os"
	"reflect"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

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

// ingressRouteProvider exposes HTTPTriggers through networking.k8s.io Ingress
// objects. It is the RouteProvider implementation for the deprecated Ingress
// path and also serves RouteConfig.Provider == "ingress". Ingresses live in
// podNamespace (which must equal the router's namespace) and are named after
// the trigger.
type ingressRouteProvider struct {
	logger     logr.Logger
	kubeClient kubernetes.Interface
	namespace  string
}

func newIngressRouteProvider(logger logr.Logger, kubeClient kubernetes.Interface) *ingressRouteProvider {
	return &ingressRouteProvider{
		logger:     logger.WithName("ingress_provider"),
		kubeClient: kubeClient,
		namespace:  podNamespace,
	}
}

func (p *ingressRouteProvider) Name() string { return fv1.RouteProviderIngress }

// Reconcile brings the Ingress for a trigger to its desired state (level-based):
// create it when the trigger requests the ingress provider and it is missing,
// update it when its spec drifts, and delete it when the trigger requests a
// different provider or no external route.
func (p *ingressRouteProvider) Reconcile(ctx context.Context, trigger *fv1.HTTPTrigger) error {
	if desiredRouteProvider(trigger) != fv1.RouteProviderIngress {
		return p.DeleteByName(ctx, trigger.Name)
	}

	desired := util.GetIngressSpec(p.namespace, trigger)
	existing, err := p.kubeClient.NetworkingV1().Ingresses(p.namespace).Get(ctx, trigger.Name, v1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		_, err := p.kubeClient.NetworkingV1().Ingresses(p.namespace).Create(ctx, desired, v1.CreateOptions{})
		if err != nil && !k8serrors.IsAlreadyExists(err) {
			return err
		}
		p.logger.V(1).Info("created ingress successfully for trigger", "trigger", trigger.Name)
		return nil
	}
	if err != nil {
		return err
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
		if _, err := p.kubeClient.NetworkingV1().Ingresses(p.namespace).Update(ctx, existing, v1.UpdateOptions{}); err != nil {
			return err
		}
		p.logger.V(1).Info("updated ingress successfully for trigger", "trigger", trigger.Name)
	}
	return nil
}

// DeleteByName removes the Ingress with the given name from the provider's
// namespace. Idempotent: a missing Ingress is not an error, so this is safe to
// call for a trigger that never had an Ingress (e.g. on the reconciler's delete
// path, where the trigger object is already gone).
func (p *ingressRouteProvider) DeleteByName(ctx context.Context, name string) error {
	err := p.kubeClient.NetworkingV1().Ingresses(p.namespace).Delete(ctx, name, v1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}
