// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/cmd"
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
			flag.FnStreaming, flag.FnStreamingProtocol,
			flag.FnStreamingIdleTimeout, flag.FnStreamingMaxDuration,
			flag.FnExposeAsMCP, flag.FnToolDescription,
			flag.FnToolInputSchema, flag.FnToolName,
			flag.FnState, flag.FnStateKeyspace, flag.FnStateMaxKeys,
			flag.FnStateMaxValueBytes, flag.FnStateTTL,
			flag.FnStateStickySource, flag.FnStateStickyName,
			flag.FnAsyncMaxAttempts, flag.FnAsyncMaxAge,
			flag.FnAsyncOnSuccess, flag.FnAsyncOnFailure,
			flag.FnAsyncOnSuccessTopic, flag.FnAsyncOnFailureTopic,
			flag.FnOnceOnly, flag.Labels, flag.Annotation, flag.FnRetainPods,
			flag.FnProvisionedConcurrency,

			// TODO retired pkg & trigger related flags from function cmd
			flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure,
			flag.PkgOCI, flag.FnBuildCmd,

			flag.HtUrl, flag.HtPrefix, flag.HtMethod,

			// flag for newdeploy to use.
			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory,
			flag.RunTimeMaxMemory, flag.ReplicasMin,
			flag.ReplicasMax, flag.RunTimeTargetCPU,
			flag.SpecSave, flag.SpecDry},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "get",
		Aliases: []string{},
		Short:   "Get function source code",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{},
	})

	getmetaCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "getmeta",
		Aliases: []string{},
		Short:   "Get function metadata",
	}, GetMeta, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.Output},
	})

	describeCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "describe",
		Aliases: []string{},
		Short:   "Describe a function's health in one view (summary, conditions, build, pods)",
	}, Describe, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{},
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
			flag.FnStreaming, flag.FnStreamingProtocol,
			flag.FnStreamingIdleTimeout, flag.FnStreamingMaxDuration,
			flag.FnExposeAsMCP, flag.FnToolDescription,
			flag.FnToolInputSchema, flag.FnToolName,
			flag.FnState, flag.FnStateKeyspace, flag.FnStateMaxKeys,
			flag.FnStateMaxValueBytes, flag.FnStateTTL,
			flag.FnStateStickySource, flag.FnStateStickyName,
			flag.FnAsyncMaxAttempts, flag.FnAsyncMaxAge,
			flag.FnAsyncOnSuccess, flag.FnAsyncOnFailure,
			flag.FnAsyncOnSuccessTopic, flag.FnAsyncOnFailureTopic,
			flag.FnOnceOnly, flag.Labels, flag.Annotation, flag.FnRetainPods,
			flag.FnProvisionedConcurrency,

			flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure,
			flag.PkgOCI, flag.FnBuildCmd, flag.PkgForce,

			flag.RunTimeMinCPU, flag.RunTimeMaxCPU, flag.RunTimeMinMemory,
			flag.RunTimeMaxMemory, flag.ReplicasMin, flag.ReplicasMax,
			flag.RunTimeTargetCPU,

			flag.SpecSave,
		},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete a function",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List functions",
		Long:    "List all functions in a namespace if specified, else, list functions across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.AllNamespaces, flag.Output},
	})

	logsCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "log",
		Aliases: []string{"logs"},
		Short:   "Display function logs",
	}, Log, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{
			flag.FnLogFollow, flag.FnLogReverseQuery, flag.FnLogCount,
			flag.FnLogDetail, flag.FnLogPod, flag.FnLogDBType, flag.NamespacePod, flag.FnLogAllPods,
			flag.FnLogRequestID, flag.FnLogTraceID, flag.FnLogLevel},
	})

	testCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "test",
		Aliases: []string{},
		Short:   "Test a function",
	}, Test, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.HtMethod, flag.FnTestHeader, flag.FnTestBody,
			flag.FnTestQuery, flag.FnTestTimeout, flag.FnTestAsync,
			// for getting log from log database if we failed to get logs from function pod.
			flag.FnLogDBType,
			flag.FnSubPath,
		},
	})

	runLocalCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "run-local",
		Aliases: []string{"runl"},
		Short:   "Alpha: Run a function locally in Docker (RFC-0018)",
		Long: "Alpha: Run a function locally in Docker — no cluster round-trip. For poolmgr/newdeploy " +
			"(--executor, default poolmgr) it runs the environment runtime image (from --env or --image) and " +
			"replays the specialize contract over --code (single file) or --deploy (a pre-built directory for " +
			"multi-file apps); for --executor=container it runs the user's own --image server directly. Either " +
			"way it invokes the function the same way the cluster does.",
		// Cluster-optional: `--image` runs entirely cluster-less (no kubeconfig
		// needed); --env / --secret / --configmap require a cluster and error
		// clearly when one is unavailable.
		Annotations: map[string]string{cmd.ClusterOptionalAnnotation: "true"},
	}, Run, flag.FlagSet{
		Optional: []flag.Flag{
			flag.FnName, flag.FnExecutorType, flag.PkgCode, flag.PkgDeployArchive,
			flag.FnEnvName, flag.FnImageName,
			flag.FnRunEnvVersion, flag.FnEntryPoint, flag.FnPort,
			flag.HtMethod, flag.FnTestHeader, flag.FnTestBody, flag.FnSubPath,
			flag.FnRunKeep, flag.FnRunWatch, flag.FnRunEnvVar, flag.FnRunEnvFile,
			flag.FnSecret, flag.FnCfgMap, flag.FnRunDebugPort,
			flag.FnRunBuild, flag.FnRunBuilderImage, flag.FnBuildCmd,
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

			flag.SpecSave, flag.SpecDry,
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

			flag.SpecSave,
		},
	})

	listPodsCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "pods",
		Aliases: []string{"pod", "po"},
		Short:   "List pods currently used by a function",
		Long:    "List pods currently used by a function",
	}, ListPods, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{},
	})

	waitCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "wait",
		Short: "Wait for a function to reach a status condition",
	}, Wait, flag.FlagSet{
		Required: []flag.Flag{flag.FnName, flag.WaitFor},
		Optional: []flag.Flag{flag.WaitTimeout},
	})

	toolsCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "tools",
		Short: "List functions exposed as MCP (Model Context Protocol) tools",
	}, Tools, flag.FlagSet{
		Optional: []flag.Flag{flag.Output},
	})

	publishCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "publish",
		Short: "Publish the function's current spec as an immutable FunctionVersion (RFC-0025)",
	}, Publish, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.PublishDescription, flag.PublishWait, flag.WaitTimeout, flag.Output},
	})

	versionsCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "versions",
		Short: "List a function's published FunctionVersions (RFC-0025)",
	}, Versions, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.Output},
	})

	command := &cobra.Command{
		Use:     "function",
		Aliases: []string{"fn"},
		Short:   "Create, update and manage functions",
	}
	command.AddCommand(createCmd, getCmd, getmetaCmd, describeCmd, updateCmd, deleteCmd, listCmd, logsCmd, testCmd,
		runLocalCmd, runContainerCmd, updateContainerCmd, listPodsCmd, waitCmd, toolsCmd, publishCmd, versionsCmd,
		DLQCommands(), StateCommands())

	return command
}
