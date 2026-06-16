// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func Enable(input cli.Input) error {
	return (&EnableSubCommand{}).do(input)
}

type EnableSubCommand struct {
	cmd.CommandActioner
}

func (opts *EnableSubCommand) do(input cli.Input) error {
	namespace := input.String(flagkey.Namespace)
	if namespace == "" {
		return fmt.Errorf("--%s is required: the namespace to onboard", flagkey.Namespace)
	}
	ctx := input.Context()
	tenants := opts.Client().FissionClientSet.CoreV1().FissionTenants()

	fnNS := input.String(flagkey.TenantFunctionNamespace)
	builderNS := input.String(flagkey.TenantBuilderNamespace)

	// Idempotent: update mutable fields if the tenant already exists.
	existing, err := tenants.Get(ctx, namespace, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Spec.FunctionNamespace = fnNS
		existing.Spec.BuilderNamespace = builderNS
		if _, err := tenants.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("error updating tenant %q: %w", namespace, err)
		}
		fmt.Printf("tenant %q updated\n", namespace)
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("error checking tenant %q: %w", namespace, err)
	}

	ft := &v1.FissionTenant{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
		Spec: v1.FissionTenantSpec{
			Namespace:         namespace,
			FunctionNamespace: fnNS,
			BuilderNamespace:  builderNS,
		},
	}
	if _, err := tenants.Create(ctx, ft, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("error enabling tenant %q: %w", namespace, err)
	}
	fmt.Printf("tenant %q enabled; run 'fission tenant status -n %s' to check readiness\n", namespace, namespace)
	return nil
}
