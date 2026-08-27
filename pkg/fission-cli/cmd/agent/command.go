// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package agent implements the `fission agent` command group: scaffold an
// agent (create.go), discover and inspect agent-declared functions
// (FunctionSpec.Agent != nil), and inspect their sessions via `fission agent
// sessions` (sessions.go), which talks to the agentruntime's RegistryAPI
// introspection endpoints (slice 4). Session archival is not implemented
// here. Creation is `fission agent create ...` (a one-command scaffold: a
// handler template plus the Package/Function it needs) or, for a function
// that already has code, `fission fn create --agent ...`.
package agent

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

// agentCreateStateFlag / agentCreateAgentFlag default ON: unlike `fn create`,
// where --state/--agent are explicit opt-ins onto an otherwise plain
// function, `agent create`'s entire purpose is scaffolding an agent, so
// State and Agent are on by default here, and --state=false/--agent=false
// opts back out (mirroring the shared helpers' own off-switch semantics,
// cmd/function/create.go's GetStateConfig/GetAgentConfig). These are local
// copies of the shared flag.FnState/flag.FnAgent descriptors with a
// different DefaultBool -- the shared vars themselves must keep their
// default-off behavior for `fn create`.
var (
	agentCreateStateFlag = withDefaultBool(flag.FnState, true)
	agentCreateAgentFlag = withDefaultBool(flag.FnAgent, true)
)

// withDefaultBool returns a copy of f with DefaultBool overridden. flag.Flag is
// a value type, so the shared descriptor (and its default-off behavior for
// `fn create`) is left untouched.
func withDefaultBool(f flag.Flag, v bool) flag.Flag {
	f.DefaultBool = v
	return f
}

// Commands returns the `fission agent` command group.
func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Scaffold a working stateful agent: handler template + Package + Function, in one command",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{
			flag.FnEnvName, flag.FnAgentLang, flag.PkgCode,
			agentCreateStateFlag, flag.FnStateKeyspace, flag.FnStateMaxKeys,
			flag.FnStateMaxValueBytes, flag.FnStateTTL,
			flag.FnStateStickySource, flag.FnStateStickyName,
			agentCreateAgentFlag, flag.FnAgentSessionSource, flag.FnAgentSessionName,
			flag.FnAgentIdleAfter, flag.FnAgentArchiveAfter, flag.FnAgentMaxSessions,
			flag.FnAgentHistoryTrim,
			flag.SpecSave,
		},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List functions declared as agents",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.Output},
	})
	// Namespace comes from the global flag.Namespace, exactly like
	// `fission fn tools` (cmd/function/command.go:274-279) — there is no
	// flag.NamespaceFunction; do not invent one. `create` above and
	// `sessions` (sessions.go) both read it the same way.

	command := &cobra.Command{
		Use:   "agent",
		Short: "Inspect and scaffold agent-declared functions",
	}
	command.AddCommand(createCmd, listCmd, SessionCommands())
	return command
}
