// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// RouteProvider reconciles the external route object (Ingress, Gateway API
// HTTPRoute, …) that exposes an HTTPTrigger outside the cluster. Providers are
// level-based and idempotent: the router registers all enabled providers and
// calls Reconcile on each for every trigger. A provider creates/updates its
// object only when the trigger requests that provider, and otherwise deletes
// any object it owns for the trigger — so toggling exposure off or switching
// providers self-cleans without the reconciler tracking prior state.
type RouteProvider interface {
	// Name is the provider's RouteConfig.Provider value ("ingress"|"gateway").
	Name() string
	// Reconcile brings this provider's route object for the trigger to its
	// desired state: created/updated when the trigger requests this provider,
	// deleted otherwise.
	Reconcile(ctx context.Context, trigger *fv1.HTTPTrigger) error
	// DeleteByName removes any object this provider owns for a trigger of the
	// given name. Idempotent (a missing object is not an error), so it is safe
	// to call on the reconciler's delete path where the trigger is already gone.
	DeleteByName(ctx context.Context, name string) error
}

// desiredRouteProvider returns the name of the provider that should own this
// trigger's external route, or "" when the trigger requests none. RouteConfig
// (the forward API) takes precedence over the deprecated CreateIngress flag.
func desiredRouteProvider(trigger *fv1.HTTPTrigger) string {
	if trigger.Spec.RouteConfig != nil {
		return trigger.Spec.RouteConfig.Provider
	}
	if trigger.Spec.CreateIngress {
		return fv1.RouteProviderIngress
	}
	return ""
}
