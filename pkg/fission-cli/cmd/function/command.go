/*
Copyright 2019 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package function

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a function (and optionally, an HTTP route to it)",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{
			flag.FnEnvName, flag.FnEntryPoint, flag.FnPkgName,
			flag.FnExecutorType, flag.FnCfgMap, flag.FnSecret,
			flag.FnSpecializationTimeout, flag.FnExecutionTimeout,
			flag.FnIdleTimeout, flag.FnConcurrency, flag.FnRequestsPerPod,
			flag.FnOnceOnly, flag.Labels, flag.Annotation, flag.FnRetainPods,

			// TODO retired pkg & trigger related flags from function cmd
			flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure,
			flag.FnBuildCmd,

			flag.HtUrl, flag.HtPrefix, flag.HtMethod,

			// flag for newdeploy to use.
			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory,
			flag.RunTimeMaxMemory, flag.ReplicasMin,
			flag.ReplicasMax, flag.RunTimeTargetCPU,
			flag.NamespaceFunction, flag.SpecSave, flag.SpecDry},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "get",
		Aliases: []string{},
		Short:   "Get function source code",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.NamespaceFunction},
	})

	getmetaCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "getmeta",
		Aliases: []string{},
		Short:   "Get function metadata",
	}, GetMeta, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.NamespaceFunction},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "update",
		Aliases: []string{},
		Short:   "Update a function",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{
			flag.FnEnvName, flag.FnEntryPoint, flag.FnPkgName,
			flag.FnExecutorType, flag.FnSecret, flag.FnCfgMap,
			flag.FnSpecializationTimeout, flag.FnExecutionTimeout,
			flag.FnIdleTimeout, flag.FnConcurrency, flag.FnRequestsPerPod,
			flag.FnOnceOnly, flag.Labels, flag.Annotation, flag.FnRetainPods,

			flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure,
			flag.FnBuildCmd, flag.PkgForce,

			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory,
			flag.RunTimeMaxMemory, flag.ReplicasMin, flag.ReplicasMax,
			flag.RunTimeTargetCPU,

			flag.NamespaceFunction, flag.NamespaceEnvironment, flag.SpecSave,
		},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete a function",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.NamespaceFunction, flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List functions",
		Long:    "List all functions in a namespace if specified, else, list functions across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceFunction, flag.AllNamespaces},
	})

	logsCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "log",
		Aliases: []string{"logs"},
		Short:   "Display function logs",
	}, Log, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{
			flag.FnLogFollow, flag.FnLogReverseQuery, flag.FnLogCount,
			flag.FnLogDetail, flag.FnLogPod, flag.NamespaceFunction, flag.FnLogDBType, flag.NamespacePod, flag.FnLogAllPods},
	})

	testCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "test",
		Aliases: []string{},
		Short:   "Test a function",
	}, Test, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.HtMethod, flag.FnTestHeader, flag.FnTestBody,
			flag.FnTestQuery, flag.FnTestTimeout, flag.NamespaceFunction,
			// for getting log from log database if
			// we failed to get logs from function pod.
			flag.FnLogDBType,
			flag.FnSubPath,
		},
	})

	runContainerCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "run-container",
		Aliases: []string{"runc"},
		Short:   "Alpha: Run a container image as a function",
	}, RunContainer, flag.FlagSet{
		Required: []flag.Flag{flag.FnName, flag.FnImageName},
		Optional: []flag.Flag{
			flag.FnPort, flag.FnCommand, flag.FnArgs,
			flag.FnCfgMap, flag.FnSecret,
			flag.FnExecutionTimeout,
			flag.FnIdleTimeout,
			flag.FnTerminationGracePeriod,
			flag.Labels, flag.Annotation,

			// flag for newdeploy to use.
			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory,
			flag.RunTimeMaxMemory, flag.ReplicasMin,
			flag.ReplicasMax, flag.RunTimeTargetCPU,
			flag.RunImagePullSecret,

			flag.NamespaceFunction, flag.SpecSave, flag.SpecDry,
		},
	})

	updateContainerCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "update-container",
		Aliases: []string{"updatec"},
		Short:   "Alpha: Update a function running a container",
	}, UpdateContainer, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{
			flag.FnImageName, flag.FnPort,
			flag.FnCommand, flag.FnArgs,
			flag.FnSecret, flag.FnCfgMap,
			flag.FnExecutionTimeout, flag.FnIdleTimeout,
			flag.Labels, flag.Annotation,

			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory,
			flag.RunTimeMaxMemory, flag.ReplicasMin, flag.ReplicasMax,
			flag.RunTimeTargetCPU,

			flag.NamespaceFunction, flag.SpecSave,
		},
	})

	listPodsCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "pods",
		Aliases: []string{"pod", "po"},
		Short:   "List pods currently used by a function",
		Long:    "List pods currently used by a function",
	}, ListPods, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.NamespaceFunction},
	})

	command := &cobra.Command{
		Use:     "function",
		Aliases: []string{"fn"},
		Short:   "Create, update and manage functions",
	}
	command.AddCommand(createCmd, getCmd, getmetaCmd, updateCmd, deleteCmd, listCmd, logsCmd, testCmd,
		runContainerCmd, updateContainerCmd, listPodsCmd)

	return command
}
