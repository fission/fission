// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"github.com/spf13/cobra"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

// Commands builds the `fission tenant` command group for multi-namespace
// tenancy. FissionTenant is cluster-scoped, so these commands take the target
// namespace via --namespace and do not use the per-resource namespace
// resolution other commands rely on.
func Commands() *cobra.Command {
	enableCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "enable",
		Short: "Onboard a namespace as a Fission tenant",
		Long:  "Create (or update) the FissionTenant for --namespace so Fission manages functions and builders there. Idempotent.",
	}, Enable, flag.FlagSet{
		Required: []flag.Flag{flag.Namespace},
		Optional: []flag.Flag{flag.TenantFunctionNamespace, flag.TenantBuilderNamespace},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List Fission tenants",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.Output},
	})

	statusCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "status",
		Short: "Show a tenant's onboarding status and conditions",
	}, Status, flag.FlagSet{
		Required: []flag.Flag{flag.Namespace},
	})

	disableCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "disable",
		Short: "Offboard a namespace (delete its FissionTenant)",
		Long:  "Delete the FissionTenant for --namespace. Refuses if the namespace still has functions unless --force is given; user resources are left in place (they simply stop being served).",
	}, Disable, flag.FlagSet{
		Required: []flag.Flag{flag.Namespace},
		Optional: []flag.Flag{flag.TenantForce},
	})

	command := &cobra.Command{
		Use:   "tenant",
		Short: "Manage multi-namespace tenancy (onboard/offboard namespaces)",
	}
	command.AddCommand(enableCmd, listCmd, statusCmd, disableCmd)
	return command
}

// managedBySource reports how a FissionTenant came to exist, for display:
// "label" (materialized from fission.io/enabled), "helm"/"user" (authored).
func managedBySource(annotations map[string]string) string {
	if s := annotations[fv1.MANAGED_BY_LABEL]; s != "" {
		return s
	}
	return "user"
}
