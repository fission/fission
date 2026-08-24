// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package agent implements the `fission agent` command group: discover and
// inspect agent-declared functions (FunctionSpec.Agent != nil). Session
// operations (list/inspect/archive) arrive with the agentruntime introspection
// API (slice 4); creation is `fission fn create --agent ...`.
package agent

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

// Commands returns the `fission agent` command group.
func Commands() *cobra.Command {
	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List functions declared as agents",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.Output},
	})
	// Namespace comes from the global flag.Namespace, exactly like
	// `fission fn tools` (cmd/function/command.go:274-279) — there is no
	// flag.NamespaceFunction; do not invent one.

	command := &cobra.Command{
		Use:   "agent",
		Short: "Inspect agent-declared functions",
	}
	command.AddCommand(listCmd)
	return command
}
