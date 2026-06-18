// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra/helptemplate"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/archive"
	"github.com/fission/fission/pkg/fission-cli/cmd/canaryconfig"
	"github.com/fission/fission/pkg/fission-cli/cmd/check"
	"github.com/fission/fission/pkg/fission-cli/cmd/environment"
	"github.com/fission/fission/pkg/fission-cli/cmd/function"
	"github.com/fission/fission/pkg/fission-cli/cmd/httptrigger"
	"github.com/fission/fission/pkg/fission-cli/cmd/kubewatch"
	"github.com/fission/fission/pkg/fission-cli/cmd/mqtrigger"
	_package "github.com/fission/fission/pkg/fission-cli/cmd/package"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/cmd/support"
	"github.com/fission/fission/pkg/fission-cli/cmd/tenant"
	"github.com/fission/fission/pkg/fission-cli/cmd/timetrigger"
	"github.com/fission/fission/pkg/fission-cli/cmd/token"
	"github.com/fission/fission/pkg/fission-cli/cmd/version"
	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/fission-cli/flag"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	_ "github.com/fission/fission/pkg/mqtrigger/messageQueue/kafka"
)

const (
	usage = `Fission: Fast and Simple Serverless Functions for Kubernetes

 * GitHub: https://github.com/fission/fission
 * Documentation: https://fission.io/docs
`
)

func App(clientOptions cmd.ClientOptions) *cobra.Command {
	cobra.EnableCommandSorting = false

	rootCmd := &cobra.Command{
		Use:  "fission",
		Long: usage,
		//SilenceUsage: true,
		PersistentPreRunE: wrapper.Wrapper(
			func(input cli.Input) error {
				console.Verbosity = input.Int(flagkey.Verbosity)
				clientOptions.KubeContext = input.String(flagkey.KubeContext)
				// TODO: use fake rest client for offline spec generation
				// if input.IsSet(flagkey.ClientOnly) || input.IsSet(flagkey.PreCheckOnly) {
				// }
				client, err := cmd.NewClient(clientOptions)
				if err != nil {
					return fmt.Errorf("failed to get fission client: %w", err)
				}
				cmd.SetClientset(*client)
				return nil
			},
		),
	}

	// Workaround fix for not to show help command
	// https://github.com/spf13/cobra/issues/587
	rootCmd.SetHelpCommand(&cobra.Command{
		Use:    "no-help",
		Hidden: true,
	})

	wrapper.SetFlags(rootCmd, flag.FlagSet{
		Global: []flag.Flag{flag.GlobalVerbosity, flag.KubeContext, flag.Namespace},
	})

	groups := helptemplate.CommandGroups{}
	groups = append(groups, helptemplate.CreateCmdGroup("Auth Commands(Note: Authentication should be enabled to use a command in this group.)", token.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Basic Commands", environment.Commands(), _package.Commands(), function.Commands(), archive.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Trigger Commands", httptrigger.Commands(), mqtrigger.Commands(), timetrigger.Commands(), kubewatch.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Deploy Strategies Commands", canaryconfig.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Declarative Application Commands", spec.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Administration Commands", tenant.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Other Commands", support.Commands(), version.Commands(), check.Commands()))
	groups.Add(rootCmd)

	flagExposer := helptemplate.ActsAsRootCommand(rootCmd, nil, groups...)
	// show global options in usage
	flagExposer.ExposeFlags(rootCmd, flagkey.Server, flagkey.Verbosity, flagkey.KubeContext, flagkey.Namespace)

	return rootCmd
}
