// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/util"
)

// Environment variables that configure the gateway route provider. They are
// set by the Helm chart from the gatewayAPI.* values.
const (
	// envGatewayAPIEnabled gates registration of the gateway route provider.
	envGatewayAPIEnabled = "GATEWAY_API_ENABLED"
	// envGatewayDefaultParentRef is an optional default Gateway parentRef used
	// for triggers that request the gateway provider without listing their own.
	// Format: "name" or "namespace/name".
	envGatewayDefaultParentRef = "GATEWAY_DEFAULT_PARENTREF"
)

// buildRouteProviders assembles the route providers the router runs. The
// ingress provider is always present (it serves the deprecated CreateIngress
// path and RouteConfig.Provider == "ingress"); the gateway provider is added
// only when GATEWAY_API_ENABLED is set, so the gateway.networking.k8s.io RBAC
// is required only when an operator opts in.
func buildRouteProviders(logger logr.Logger, kubeClient kubernetes.Interface, restConfig *rest.Config) ([]RouteProvider, error) {
	providers := []RouteProvider{newIngressRouteProvider(logger, kubeClient)}

	if enabled, _ := strconv.ParseBool(os.Getenv(envGatewayAPIEnabled)); enabled {
		gwClient, err := gatewayclient.NewForConfig(restConfig)
		if err != nil {
			return nil, fmt.Errorf("building gateway api client: %w", err)
		}
		defaultRefs, err := parseDefaultParentRefs(os.Getenv(envGatewayDefaultParentRef))
		if err != nil {
			return nil, err
		}
		providers = append(providers, newGatewayRouteProvider(logger, gwClient, defaultRefs))
		logger.Info("gateway api route provider enabled", "defaultParentRefs", len(defaultRefs))
	}

	return providers, nil
}

// parseDefaultParentRefs parses the GATEWAY_DEFAULT_PARENTREF env value into a
// single default parentRef. An empty value yields no default (triggers must
// then specify their own parentRefs). The value is "name" or "namespace/name".
func parseDefaultParentRefs(raw string) ([]gwapiv1.ParentReference, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	ref := gwapiv1.ParentReference{}
	if ns, name, ok := strings.Cut(raw, "/"); ok {
		if ns == "" || name == "" {
			return nil, fmt.Errorf("invalid %s %q: expected \"name\" or \"namespace/name\"", envGatewayDefaultParentRef, raw)
		}
		ref.Namespace = new(gwapiv1.Namespace(ns))
		ref.Name = gwapiv1.ObjectName(name)
	} else {
		ref.Name = gwapiv1.ObjectName(raw)
	}
	return []gwapiv1.ParentReference{ref}, nil
}

// gatewayRouteProvider exposes HTTPTriggers through Gateway API HTTPRoute
// objects attached to an operator-managed Gateway (attach mode). HTTPRoutes
// live in podNamespace (the router's namespace) and are named after the
// trigger.
type gatewayRouteProvider struct {
	logger            logr.Logger
	client            gatewayclient.Interface
	namespace         string
	defaultParentRefs []gwapiv1.ParentReference
}

func newGatewayRouteProvider(logger logr.Logger, client gatewayclient.Interface, defaultParentRefs []gwapiv1.ParentReference) *gatewayRouteProvider {
	return &gatewayRouteProvider{
		logger:            logger.WithName("gateway_provider"),
		client:            client,
		namespace:         podNamespace,
		defaultParentRefs: defaultParentRefs,
	}
}

func (p *gatewayRouteProvider) Name() string { return fv1.RouteProviderGateway }

// Reconcile brings the HTTPRoute for a trigger to its desired state
// (level-based): create it when the trigger requests the gateway provider and
// it is missing, update it when its spec/annotations drift, and delete it when
// the trigger requests a different provider or no external route.
func (p *gatewayRouteProvider) Reconcile(ctx context.Context, trigger *fv1.HTTPTrigger) error {
	if desiredRouteProvider(trigger) != fv1.RouteProviderGateway {
		return p.DeleteByName(ctx, trigger.Name)
	}

	desired := util.GetHTTPRouteSpec(p.namespace, trigger, p.defaultParentRefs)
	if len(desired.Spec.ParentRefs) == 0 {
		return fmt.Errorf("trigger %q requests the gateway provider but no parentRefs are set and the router has no default Gateway configured", trigger.Name)
	}

	existing, err := p.client.GatewayV1().HTTPRoutes(p.namespace).Get(ctx, trigger.Name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		_, err := p.client.GatewayV1().HTTPRoutes(p.namespace).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil && !k8serrors.IsAlreadyExists(err) {
			return err
		}
		p.logger.V(1).Info("created httproute successfully for trigger", "trigger", trigger.Name)
		return nil
	}
	if err != nil {
		return err
	}

	changes := false
	if !reflect.DeepEqual(existing.Annotations, desired.Annotations) {
		existing.Annotations = desired.Annotations
		changes = true
	}
	if !reflect.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		changes = true
	}
	if changes {
		if _, err := p.client.GatewayV1().HTTPRoutes(p.namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return err
		}
		p.logger.V(1).Info("updated httproute successfully for trigger", "trigger", trigger.Name)
	}
	return nil
}

// DeleteByName removes the HTTPRoute with the given name from the provider's
// namespace. Idempotent: a missing HTTPRoute is not an error.
func (p *gatewayRouteProvider) DeleteByName(ctx context.Context, name string) error {
	err := p.client.GatewayV1().HTTPRoutes(p.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}
