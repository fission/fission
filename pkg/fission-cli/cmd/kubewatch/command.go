// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatch

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a kube watcher",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.KwFnName},
		Optional: []flag.Flag{flag.KwName, flag.KwObjType, flag.SpecSave, flag.SpecDry},
		// TODO: add label selector flag
		// flag.KwLabelsFlag
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete a kube watcher",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.KwName},
		Optional: []flag.Flag{flag.IgnoreNotFound, flag.KwFnName},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List kube watchers",
		Long:    "List all kube watchers in a namespace if specified, else, list kube watchers across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.AllNamespaces, flag.Output},
	})

	command := &cobra.Command{
		Use:     "watch",
		Aliases: []string{"w"},
		Short:   "Create, update and manage kube watcher",
	}

	waitCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "wait",
		Short: "Wait for a kube watcher to reach a status condition",
	}, Wait, flag.FlagSet{
		Required: []flag.Flag{flag.KwName, flag.WaitFor},
		Optional: []flag.Flag{flag.WaitTimeout},
	})

	command.AddCommand(createCmd, deleteCmd, listCmd, waitCmd)

	return command
}
