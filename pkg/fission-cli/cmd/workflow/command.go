// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a workflow from a manifest",
		Long:  "Create a workflow from a manifest. --name overrides the manifest's metadata.name.",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.WfFile},
		Optional: []flag.Flag{flag.WfName, flag.SpecSave, flag.SpecDry},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "update",
		Short: "Update a workflow from a manifest",
		Long:  "Update a workflow from a manifest. --name overrides the manifest's metadata.name.",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.WfFile},
		Optional: []flag.Flag{flag.WfName},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "delete",
		Short: "Delete a workflow",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.WfName},
		Optional: []flag.Flag{flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List workflows",
		Long:  "List all workflows in a namespace if specified, else, list workflows across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.AllNamespaces, flag.Output},
	})

	validateCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate a workflow manifest offline (graph, expressions), plus referenced-function existence against the cluster unless --offline",
		// Runs without a kubeconfig when --offline skips the cluster checks.
		Annotations: map[string]string{cmd.ClusterOptionalAnnotation: "true"},
	}, Validate, flag.FlagSet{
		Required: []flag.Flag{flag.WfFile},
		Optional: []flag.Flag{flag.WfName, flag.WfOffline},
	})

	graphCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "graph",
		Short: "Render a workflow's state machine as a mermaid diagram",
		// Runs without a kubeconfig when rendering from --file.
		Annotations: map[string]string{cmd.ClusterOptionalAnnotation: "true"},
	}, Graph, flag.FlagSet{
		Optional: []flag.Flag{flag.WfName, flag.WfFile, flag.WfOpen},
	})

	// run starts an execution and so acts on a Workflow (not a run) — it stays
	// at the top level with the other workflow verbs and takes a workflow --name.
	runCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "run",
		Short: "Start one execution of a workflow",
	}, Run, flag.FlagSet{
		Required: []flag.Flag{flag.WfName},
		Optional: []flag.Flag{flag.WfInput},
	})

	// The `runs` subgroup operates on WorkflowRuns. Its subcommands take a run
	// --name (WfRunName), so the flag help reads "Name of the workflow run"
	// instead of the workflow-scoped help — the source of the run-vs-workflow
	// confusion when these were flat siblings of the workflow verbs.
	runsListCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List workflow runs",
	}, Runs, flag.FlagSet{
		Optional: []flag.Flag{flag.WfWorkflow, flag.AllNamespaces, flag.Output},
	})

	runsDescribeCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "describe",
		Short: "Answer \"where did this run stop\": phase, active state, last error, attempts",
	}, Describe, flag.FlagSet{
		Required: []flag.Flag{flag.WfRunName},
	})

	runsHistoryCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "history",
		Short: "Show a run's full step-level event history",
	}, History, flag.FlagSet{
		Required: []flag.Flag{flag.WfRunName},
		Optional: []flag.Flag{flag.WfIO},
	})

	runsCancelCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "cancel",
		Short: "Request cancellation of a workflow run (in-flight steps drain)",
	}, Cancel, flag.FlagSet{
		Required: []flag.Flag{flag.WfRunName},
	})

	runsGraphCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "graph",
		Short: "Render a run's state machine with each state colored by what the run did",
		Long: "Render a run's state machine as a mermaid diagram, coloring each state by what this run did: " +
			"succeeded, active, failed, or never reached. Drawn against the spec snapshot the run is executing, " +
			"so it stays accurate even if the workflow was edited or deleted since.",
	}, RunsGraph, flag.FlagSet{
		Required: []flag.Flag{flag.WfRunName},
		Optional: []flag.Flag{flag.WfOpen},
	})

	runsCmd := &cobra.Command{
		Use:   "runs",
		Short: "List and inspect workflow runs (executions)",
	}
	runsCmd.AddCommand(runsListCmd, runsDescribeCmd, runsHistoryCmd, runsCancelCmd, runsGraphCmd)

	command := &cobra.Command{
		Use:     "workflow",
		Aliases: []string{"wf"},
		Short:   "Create, update and manage workflows",
	}

	command.AddCommand(createCmd, updateCmd, deleteCmd, listCmd, validateCmd, graphCmd,
		runCmd, runsCmd)

	return command
}
