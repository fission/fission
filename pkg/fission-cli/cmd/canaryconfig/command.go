// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

// Commands returns canary config commands
func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a canary config",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName, flag.CanaryTriggerName, flag.CanaryNewFunc, flag.CanaryOldFunc},
		Optional: []flag.Flag{flag.CanaryWeightIncrement, flag.CanaryIncrementInterval, flag.CanaryFailureThreshold},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "get",
		Aliases: []string{},
		Short:   "View parameters in a canary config",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.Output},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "update",
		Aliases: []string{},
		Short:   "Update parameters of a canary config",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.CanaryWeightIncrement, flag.CanaryIncrementInterval, flag.CanaryFailureThreshold},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete a canary config",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List canary configs",
		Long:    "List all canary configs in a namespace if specified, else, list canary configs across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.AllNamespaces, flag.Output},
	})

	command := &cobra.Command{
		Use:     "canary",
		Aliases: []string{"canary-config"},
		Short:   "Create, Update and manage canary configs",
	}

	waitCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "wait",
		Short: "Wait for a canary config to reach a status condition",
	}, Wait, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName, flag.WaitFor},
		Optional: []flag.Flag{flag.WaitTimeout},
	})

	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd, waitCmd)

	return command
}
