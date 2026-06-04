// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create an environment",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName, flag.EnvImage},
		Optional: []flag.Flag{
			flag.EnvPoolsize, flag.EnvBuilderImage, flag.EnvBuildCmd, flag.EnvBuilderIdleTimeout, flag.EnvBuilderPoolsize,
			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory, flag.RunTimeMaxMemory,
			flag.EnvTerminationGracePeriod, flag.EnvVersion, flag.EnvImagePullSecret, flag.EnvKeepArchive,
			flag.NamespaceEnvironment, flag.EnvExternalNetwork, flag.Labels, flag.Annotation,
			flag.SpecSave, flag.SpecDry, flag.EnvBuilder, flag.EnvRuntime},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "get",
		Short: "Get environment details",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{flag.NamespaceEnvironment},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "update",
		Short: "Update an environment",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{flag.EnvImage, flag.EnvPoolsize,
			flag.EnvBuilderImage, flag.EnvBuildCmd, flag.EnvBuilderIdleTimeout, flag.EnvBuilderPoolsize, flag.EnvImagePullSecret,
			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory, flag.RunTimeMaxMemory,
			flag.EnvTerminationGracePeriod, flag.EnvKeepArchive, flag.EnvRuntime,
			flag.NamespaceEnvironment, flag.EnvExternalNetwork,
			flag.Labels, flag.Annotation},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "delete",
		Short: "Delete an environment",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{flag.NamespaceEnvironment, flag.IgnoreNotFound, flag.EnvForce},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List environments",
		Long:  "List all environments in a namespace if specified, else, list environments across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceEnvironment, flag.AllNamespaces, flag.Output},
	})

	listPodsCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "pods",
		Aliases: []string{"pod", "po"},
		Short:   "List pods currently maintained by an environment",
		Long:    "List pods currently maintained by an environment",
	}, ListPods, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{flag.NamespaceEnvironment, flag.EnvExecutorType},
	})

	command := &cobra.Command{
		Use:     "environment",
		Aliases: []string{"env"},
		Short:   "Create, update and manage environments",
	}

	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd, listPodsCmd)

	return command
}
