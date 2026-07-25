// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package functionalias implements the `fission alias` command group: create,
// update, get, list and delete FunctionAlias objects (RFC-0025). Aliases are
// mutable, named pointers at one (or, during a weighted rollout, two)
// FunctionVersion(s) of a Function — what triggers reference in production.
package functionalias

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

// Commands returns the `fission alias` command group.
func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a function alias",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.AliasName, flag.AliasFunction},
		Optional: []flag.Flag{
			flag.AliasVersion, flag.AliasPackageDigest, flag.AliasWeight, flag.AliasSecondaryVersion,
			flag.AliasWait, flag.WaitTimeout,
		},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "get",
		Short: "Get a function alias",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.AliasName},
		Optional: []flag.Flag{flag.Output},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "update",
		Short: "Update a function alias",
		Long: "Update a FunctionAlias's target (--version or --package-digest, mutually exclusive) or " +
			"traffic split (--weight/--secondary-version, or --clear-weight to drop it). To repoint back to " +
			"a previously resolved target using the alias's own Status.History instead of naming a version " +
			"by hand, see `fission fn rollback` instead.",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.AliasName},
		Optional: []flag.Flag{
			flag.AliasVersion, flag.AliasPackageDigest, flag.AliasWeight, flag.AliasSecondaryVersion, flag.AliasClearWeight,
			flag.AliasWait, flag.WaitTimeout,
		},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "delete",
		Short: "Delete a function alias",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.AliasName},
		Optional: []flag.Flag{flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List function aliases",
		Long:  "List all function aliases in a namespace if specified, else, list function aliases across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.AliasFunction, flag.AllNamespaces, flag.Output},
	})

	waitCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "wait",
		Short: "Wait for a function alias to reach a status condition",
		Long: "Wait for a FunctionAlias to reach a status condition, e.g. `fission alias wait --name prod " +
			"--for condition=Resolved` after an `alias create`/`alias update`/`fn rollback`, so a caller " +
			"(CI, a script) can tell when the alias resolver has actually converged on the new target " +
			"rather than racing it. FunctionAlias's only condition type is Resolved.",
	}, Wait, flag.FlagSet{
		Required: []flag.Flag{flag.AliasName, flag.WaitFor},
		Optional: []flag.Flag{flag.WaitTimeout},
	})

	command := &cobra.Command{
		Use:   "alias",
		Short: "Create, update and manage function aliases",
	}
	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd, waitCmd)

	return command
}
