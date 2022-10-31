/*
Copyright 2022 The Fission Authors.
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

package app

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/controller/client/rest"
	"github.com/fission/fission/pkg/crd"
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
	"github.com/fission/fission/pkg/fission-cli/cmd/timetrigger"
	"github.com/fission/fission/pkg/fission-cli/cmd/token"
	"github.com/fission/fission/pkg/fission-cli/cmd/version"
	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/fission-cli/flag"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	_ "github.com/fission/fission/pkg/mqtrigger/messageQueue/kafka"
)

const (
	usage = `Fission: Fast and Simple Serverless Functions for Kubernetes

 * GitHub: https://github.com/fission/fission
 * Documentation: https://fission.io/docs
`
)

func App() *cobra.Command {
	cobra.EnableCommandSorting = false

	rootCmd := &cobra.Command{
		Use:  "fission",
		Long: usage,
		//SilenceUsage: true,
		PersistentPreRunE: wrapper.Wrapper(
			func(input cli.Input) error {
				console.Verbosity = input.Int(flagkey.Verbosity)

				if input.IsSet(flagkey.ClientOnly) || input.IsSet(flagkey.PreCheckOnly) {
					// TODO: use fake rest client for offline spec generation
					fissionClient, kubernetesClient, _, _, err := crd.MakeFissionClient() //TODO: check correct value
					if err != nil {
						return errors.Wrap(err, "failed to get fission or kubernetes client")
					}
					cmd.SetClientset(client.MakeFakeClientset(nil), fissionClient, kubernetesClient)
				} else {
					serverUrl, err := util.GetServerURL(input)
					if err != nil {
						return err
					}
					restClient := rest.NewRESTClient(serverUrl)
					fissionClient, kubernetesClient, _, _, err := crd.MakeFissionClient()
					if err != nil {
						return errors.Wrap(err, "failed to get fission or kubernetes client")
					}

					cmd.SetClientset(client.MakeClientset(restClient), fissionClient, kubernetesClient)

				}

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
		Global: []flag.Flag{flag.GlobalServer, flag.GlobalVerbosity, flag.KubeContext, flag.Namespace},
	})

	groups := helptemplate.CommandGroups{}
	groups = append(groups, helptemplate.CreateCmdGroup("Auth Commands(Note: Authentication should be enabled to use a command in this group.)", token.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Basic Commands", environment.Commands(), _package.Commands(), function.Commands(), archive.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Trigger Commands", httptrigger.Commands(), mqtrigger.Commands(), timetrigger.Commands(), kubewatch.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Deploy Strategies Commands", canaryconfig.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Declarative Application Commands", spec.Commands()))
	groups = append(groups, helptemplate.CreateCmdGroup("Other Commands", support.Commands(), version.Commands(), check.Commands()))
	groups.Add(rootCmd)

	flagExposer := helptemplate.ActsAsRootCommand(rootCmd, nil, groups...)
	// show global options in usage
	flagExposer.ExposeFlags(rootCmd, flagkey.Server, flagkey.Verbosity, flagkey.KubeContext, flagkey.Namespace)

	return rootCmd
}
