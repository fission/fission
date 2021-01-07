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
	createCmd := &cobra.Command{
		Use:     "create [function name]",
		Short:   "Create a function (and optionally, an HTTP route to it)",
		RunE:    wrapper.Wrapper(Create),
		Args:    cobra.MinimumNArgs(1),
		Example: "fission function create func-name --env nodejs --code hello.js",
	}
	wrapper.SetFlags(createCmd, flag.FlagSet{
		Optional: []flag.Flag{
			flag.FnEnvName, flag.FnEntryPoint, flag.FnPkgName,
			flag.FnExecutorType, flag.FnCfgMap, flag.FnSecret,
			flag.FnSpecializationTimeout, flag.FnExecutionTimeout,
			flag.FnIdleTimeout, flag.FnConcurrency,

			// TODO retired pkg & trigger related flags from function cmd
			flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure,
			flag.FnBuildCmd,

			flag.HtUrl, flag.HtMethod,

			// flag for new deploy to use.
			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory,
			flag.RunTimeMaxMemory, flag.ReplicasMin,
			flag.ReplicasMax, flag.RunTimeTargetCPU,

			flag.NamespaceFunction, flag.NamespaceEnvironment, flag.SpecSave, flag.SpecDry},
	})

	getCmd := &cobra.Command{
		Use:     "get [function name]",
		Aliases: []string{},
		Short:   "Get function source code",
		RunE:    wrapper.Wrapper(Get),
		Args:    cobra.MinimumNArgs(1),
		Example: "fission function get func-name",
	}
	wrapper.SetFlags(getCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceFunction},
	})

	getmetaCmd := &cobra.Command{
		Use:     "getmeta [function name]",
		Aliases: []string{},
		Short:   "Get function metadata",
		RunE:    wrapper.Wrapper(GetMeta),
		Args:    cobra.MinimumNArgs(1),
		Example: "fission function getmeta func-name",
	}
	wrapper.SetFlags(getmetaCmd, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.NamespaceFunction},
	})

	updateCmd := &cobra.Command{
		Use:     "update [function name]",
		Aliases: []string{},
		Short:   "Update a function",
		RunE:    wrapper.Wrapper(Update),
		Args:    cobra.MinimumNArgs(1),
		Example: "fission function update func-name --env nodejs --code hello.js",
	}
	wrapper.SetFlags(updateCmd, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{
			flag.FnEnvName, flag.FnEntryPoint, flag.FnPkgName,
			flag.FnExecutorType, flag.FnSecret, flag.FnCfgMap,
			flag.FnSpecializationTimeout, flag.FnExecutionTimeout,
			flag.FnIdleTimeout, flag.FnConcurrency,

			flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure,
			flag.FnBuildCmd, flag.PkgForce,

			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory,
			flag.RunTimeMaxMemory, flag.ReplicasMin, flag.ReplicasMax,
			flag.RunTimeTargetCPU,

			flag.NamespaceFunction, flag.NamespaceEnvironment, flag.SpecSave,
		},
	})

	deleteCmd := &cobra.Command{
		Use:     "delete [function name]",
		Aliases: []string{},
		Short:   "Delete a function",
		RunE:    wrapper.Wrapper(Delete),
		Args:    cobra.MinimumNArgs(1),
		Example: "fission function delete func-name",
	}
	wrapper.SetFlags(deleteCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceFunction},
	})

	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List functions",
		Long:    "List all functions in a namespace if specified, else, list functions across all namespaces",
		RunE:    wrapper.Wrapper(List),
		Example: "fission function list",
	}
	wrapper.SetFlags(listCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceFunction},
	})

	logsCmd := &cobra.Command{
		Use:     "log [function name]",
		Aliases: []string{"logs"},
		Short:   "Display function logs",
		RunE:    wrapper.Wrapper(Log),
		Args:    cobra.MinimumNArgs(1),
		Example: "fission function log func-name",
	}
	wrapper.SetFlags(logsCmd, flag.FlagSet{
		Optional: []flag.Flag{
			flag.FnLogFollow, flag.FnLogReverseQuery, flag.FnLogCount,
			flag.FnLogDetail, flag.FnLogPod, flag.NamespaceFunction, flag.FnLogDBType},
	})

	testCmd := &cobra.Command{
		Use:     "test [function name]",
		Aliases: []string{},
		Short:   "Test a function",
		RunE:    wrapper.Wrapper(Test),
		Args:    cobra.MinimumNArgs(1),
		Example: "fission function test func-name",
	}
	wrapper.SetFlags(testCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.HtMethod, flag.FnTestHeader, flag.FnTestBody,
			flag.FnTestQuery, flag.FnTestTimeout, flag.NamespaceFunction,
			// for getting log from log database if
			// we failed to get logs from function pod.
			flag.FnLogDBType,
		},
	})

	command := &cobra.Command{
		Use:     "function",
		Aliases: []string{"fn"},
		Short:   "Create, update and manage functions",
	}

	command.AddCommand(createCmd, getCmd, getmetaCmd, updateCmd, deleteCmd, listCmd, logsCmd, testCmd)

	return command
}
