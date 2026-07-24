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
			flag.EnvPoolsize, flag.EnvBuilderImage, flag.EnvBuildCmd,
			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory, flag.RunTimeMaxMemory,
			flag.EnvTerminationGracePeriod, flag.EnvVersion, flag.EnvImagePullSecret, flag.EnvKeepArchive,
			flag.EnvExternalNetwork, flag.Labels, flag.Annotation,
			flag.SpecSave, flag.SpecDry, flag.EnvBuilder, flag.EnvRuntime},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "get",
		Short: "Get environment details",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "update",
		Short: "Update an environment",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{flag.EnvImage, flag.EnvPoolsize,
			flag.EnvBuilderImage, flag.EnvBuildCmd, flag.EnvImagePullSecret,
			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory, flag.RunTimeMaxMemory,
			flag.EnvTerminationGracePeriod, flag.EnvKeepArchive, flag.EnvRuntime,
			flag.EnvExternalNetwork,
			flag.Labels, flag.Annotation},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "delete",
		Short: "Delete an environment",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{flag.IgnoreNotFound, flag.EnvForce},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List environments",
		Long:  "List all environments in a namespace if specified, else, list environments across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.AllNamespaces, flag.Output},
	})

	listPodsCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "pods",
		Aliases: []string{"pod", "po"},
		Short:   "List pods currently maintained by an environment",
		Long:    "List pods currently maintained by an environment",
	}, ListPods, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{flag.EnvExecutorType},
	})

	impactCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "impact",
		Short: "Show functions and aliases affected by this environment, and their env-drift status",
		Long: "List every function that references this environment and, for each of its aliases, whether the " +
			"alias's resolved version was published under an environment generation the live environment has " +
			"since moved past (RFC-0025 env drift) — the batch, ahead-of-an-update view of `fission fn describe`'s " +
			"per-alias EnvDrift condition.",
	}, Impact, flag.FlagSet{
		Required: []flag.Flag{flag.EnvName},
		Optional: []flag.Flag{flag.Output},
	})

	command := &cobra.Command{
		Use:     "environment",
		Aliases: []string{"env"},
		Short:   "Create, update and manage environments",
	}

	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd, listPodsCmd, impactCmd)

	return command
}
