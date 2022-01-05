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

package httptrigger

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create an HTTP trigger",
		RunE:  wrapper.Wrapper(Create),
	}
	wrapper.SetFlags(createCmd, flag.FlagSet{
		Required: []flag.Flag{flag.HtFnName},
		Optional: []flag.Flag{flag.HtUrl, flag.HtName, flag.HtMethod, flag.HtIngress,
			flag.HtIngressRule, flag.HtIngressAnnotation, flag.HtIngressTLS,
			flag.HtFnWeight, flag.HtHost, flag.NamespaceFunction, flag.SpecSave, flag.SpecDry,
			flag.HtPrefix, flag.HtKeepPrefix},
	})

	getCmd := &cobra.Command{
		Use:     "get",
		Aliases: []string{},
		Short:   "Get HTTP trigger details",
		RunE:    wrapper.Wrapper(Get),
	}
	wrapper.SetFlags(getCmd, flag.FlagSet{
		Required: []flag.Flag{flag.HtName},
	})

	updateCmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{},
		Short:   "Update an HTTP trigger",
		RunE:    wrapper.Wrapper(Update),
	}
	wrapper.SetFlags(updateCmd, flag.FlagSet{
		Required: []flag.Flag{flag.HtName},
		Optional: []flag.Flag{flag.HtUrl, flag.HtFnName,
			flag.HtMethod, flag.HtIngress, flag.HtIngressRule, flag.HtIngressAnnotation,
			flag.HtIngressTLS, flag.HtFnWeight, flag.HtHost, flag.NamespaceTrigger,
			flag.HtPrefix, flag.HtKeepPrefix},
	})

	deleteCmd := &cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete an HTTP trigger",
		RunE:    wrapper.Wrapper(Delete),
	}
	wrapper.SetFlags(deleteCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.HtName, flag.HtFnFilter, flag.NamespaceTrigger, flag.IgnoreNotFound},
	})

	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List HTTP triggers",
		Long:    "List all HTTP triggers in a namespace if specified, else, list HTTP triggers across all namespaces",
		RunE:    wrapper.Wrapper(List),
	}
	wrapper.SetFlags(listCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceTrigger, flag.HtFnFilter},
	})

	command := &cobra.Command{
		Use:     "httptrigger",
		Aliases: []string{"ht", "route"},
		Short:   "Create, update and manage HTTP triggers",
	}

	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd)

	return command
}
