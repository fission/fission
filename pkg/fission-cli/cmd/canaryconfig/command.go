// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

// Commands returns canary config commands
func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a canary config",
		Long: `Create a canary config that gradually shifts HTTP traffic from an old
target to a new one, watching the new target's Prometheus error rate and
rolling back automatically if it crosses --threshold.

Two modes, selected by what --httptrigger references:

  function-weights mode (classic): the trigger's function reference type is
  "function-weights" and --newfn/--oldfn name two FUNCTIONS already present
  in its weights map. The controller steps HTTPTrigger.FunctionWeights.

  alias mode (RFC-0025): the trigger references a FunctionAlias (created via
  'fission alias create'). --newfn/--oldfn then name two FunctionVersions
  (see 'fission fn versions') of the alias's function — not functions. The
  controller steps the FunctionAlias's Weight/SecondaryVersion instead,
  leaving the alias's primary Version pinned at --oldfn until the rollout
  either promotes (repoints the alias at --newfn) or rolls back.

Example (alias mode):

  fission alias create --function orders --name prod --version orders-v3
  fission canary create --name orders-canary --httptrigger prod-route \
    --newfn orders-v4 --oldfn orders-v3 --increment-step 20 --increment-interval 2m --failure-threshold 10
`,
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName, flag.CanaryTriggerName, flag.CanaryNewFunc, flag.CanaryOldFunc},
		Optional: []flag.Flag{flag.CanaryWeightIncrement, flag.CanaryIncrementInterval, flag.CanaryFailureThreshold},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "get",
		Aliases: []string{},
		Short:   "View parameters in a canary config",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.Output},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "update",
		Aliases: []string{},
		Short:   "Update parameters of a canary config",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.CanaryWeightIncrement, flag.CanaryIncrementInterval, flag.CanaryFailureThreshold},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete a canary config",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List canary configs",
		Long:    "List all canary configs in a namespace if specified, else, list canary configs across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.AllNamespaces, flag.Output},
	})

	command := &cobra.Command{
		Use:     "canary",
		Aliases: []string{"canary-config"},
		Short:   "Create, Update and manage canary configs",
	}

	waitCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "wait",
		Short: "Wait for a canary config to reach a status condition",
	}, Wait, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName, flag.WaitFor},
		Optional: []flag.Flag{flag.WaitTimeout},
	})

	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd, waitCmd)

	return command
}
