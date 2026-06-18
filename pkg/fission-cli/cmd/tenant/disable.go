// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func Disable(input cli.Input) error {
	return (&DisableSubCommand{}).do(input)
}

type DisableSubCommand struct {
	cmd.CommandActioner
}

func (opts *DisableSubCommand) do(input cli.Input) error {
	namespace := input.String(flagkey.Namespace)
	if namespace == "" {
		return fmt.Errorf("--%s is required: the tenant namespace", flagkey.Namespace)
	}
	ctx := input.Context()
	force := input.Bool(flagkey.TenantForce)

	// Safe by default: refuse to stop managing a namespace that still has
	// functions. User Functions are left in place (they simply stop being
	// served). Finalizer-based draining of provisioned per-namespace resources
	// arrives with the provisioning phase.
	if !force {
		fns, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			// Fail closed: if we cannot confirm the namespace is empty, do not
			// disable it on a guess. The operator can --force to skip the check.
			return fmt.Errorf("checking namespace %q for functions before disabling (re-run with --force to skip): %w", namespace, err)
		}
		if len(fns.Items) > 0 {
			return fmt.Errorf("namespace %q still has %d function(s); delete them first, or re-run with --force to disable anyway (functions will stop being served)", namespace, len(fns.Items))
		}
	}

	if err := opts.Client().FissionClientSet.CoreV1().FissionTenants().Delete(ctx, namespace, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("error disabling tenant %q: %w", namespace, err)
	}
	fmt.Printf("tenant %q disabled\n", namespace)
	return nil
}
