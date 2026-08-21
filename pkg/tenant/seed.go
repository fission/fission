// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

// managedByHelm marks a FissionTenant the chart seeded from the env-driven
// namespace configuration, as opposed to "label" (materialized from
// fission.io/enabled) or a hand-authored ("user") CR.
const managedByHelm = "helm"

// SeedTenants migrates an env-configured install to the FissionTenant model: it
// creates a FissionTenant for each namespace in the resolver's set that is not
// already managed by an existing tenant (matched on spec.namespace), annotated
// managed-by=helm. The deprecated cluster-global FISSION_FUNCTION_NAMESPACE /
// FISSION_BUILDER_NAMESPACE overrides — which only ever applied to the default
// namespace — are mapped onto the default tenant's per-tenant fields.
//
// It is idempotent (create-if-absent) and never touches user- or label-created
// tenants, so it is safe to run repeatedly as a post-install/post-upgrade hook.
// It mutates no Deployment, so seeding an existing install restarts nothing.
func SeedTenants(ctx context.Context, fissionClient versioned.Interface, nsr *utils.NamespaceResolver, logger logr.Logger) error {
	tenants := fissionClient.CoreV1().FissionTenants()
	existing, err := tenants.List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing existing tenants: %w", err)
	}
	managed := make(map[string]bool, len(existing.Items))
	for i := range existing.Items {
		managed[existing.Items[i].Spec.Namespace] = true
	}

	defaultNS := nsr.DefaultNamespace
	if defaultNS == "" {
		defaultNS = metav1.NamespaceDefault
	}

	for ns := range nsr.FissionResourceNamespaces() {
		if managed[ns] {
			continue
		}
		ft := &fv1.FissionTenant{
			ObjectMeta: metav1.ObjectMeta{
				Name:        ns,
				Annotations: map[string]string{managedByAnnotation: managedByHelm},
			},
			Spec: fv1.FissionTenantSpec{Namespace: ns},
		}
		// The deprecated global overrides only ever remapped the default
		// namespace, so they migrate onto the default tenant only.
		if ns == defaultNS {
			ft.Spec.FunctionNamespace = nsr.FunctionNamespace
			ft.Spec.BuilderNamespace = nsr.BuilderNamespace
		}
		if _, err := tenants.Create(ctx, ft, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("seeding tenant for namespace %q: %w", ns, err)
		}
		logger.Info("seeded FissionTenant", "namespace", ns)
	}
	return nil
}
