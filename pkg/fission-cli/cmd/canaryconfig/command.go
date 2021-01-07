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

package canaryconfig

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

//Commands returns canary config commands
func Commands() *cobra.Command {
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a canary config",
		RunE:  wrapper.Wrapper(Create),
	}
	wrapper.SetFlags(createCmd, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName, flag.CanaryTriggerName, flag.CanaryNewFunc, flag.CanaryOldFunc},
		Optional: []flag.Flag{flag.CanaryWeightIncrement, flag.CanaryIncrementInterval, flag.CanaryFailureThreshold, flag.NamespaceFunction},
	})

	getCmd := &cobra.Command{
		Use:     "get",
		Aliases: []string{},
		Short:   "View parameters in a canary config",
		RunE:    wrapper.Wrapper(Get),
	}
	wrapper.SetFlags(getCmd, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.NamespaceCanary},
	})

	updateCmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{},
		Short:   "Update parameters of a canary config",
		RunE:    wrapper.Wrapper(Update),
	}
	wrapper.SetFlags(updateCmd, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.CanaryWeightIncrement, flag.CanaryIncrementInterval, flag.CanaryFailureThreshold, flag.NamespaceCanary},
	})

	deleteCmd := &cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete a canary config",
		RunE:    wrapper.Wrapper(Delete),
	}
	wrapper.SetFlags(deleteCmd, flag.FlagSet{
		Required: []flag.Flag{flag.CanaryName},
		Optional: []flag.Flag{flag.NamespaceCanary},
	})

	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List canary configs",
		Long:    "List all canary configs in a namespace if specified, else, list canary configs across all namespaces",
		RunE:    wrapper.Wrapper(List),
	}
	wrapper.SetFlags(listCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceCanary},
	})

	command := &cobra.Command{
		Use:     "canary",
		Aliases: []string{"canary-config"},
		Short:   "Create, Update and manage canary configs",
	}

	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd)

	return command
}
