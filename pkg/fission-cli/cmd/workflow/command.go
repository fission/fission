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
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.WfFile},
		Optional: []flag.Flag{flag.WfName, flag.SpecSave, flag.SpecDry},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "update",
		Short: "Update a workflow from a manifest",
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
		Optional: []flag.Flag{flag.WfName, flag.WfFile},
	})

	command := &cobra.Command{
		Use:     "workflow",
		Aliases: []string{"wf"},
		Short:   "Create, update and manage workflows",
	}

	command.AddCommand(createCmd, updateCmd, deleteCmd, listCmd, validateCmd, graphCmd)

	return command
}
