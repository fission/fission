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
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create an HTTP trigger",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.HtFnName},
		Optional: []flag.Flag{flag.HtUrl, flag.HtName, flag.HtMethod, flag.HtIngress,
			flag.HtIngressRule, flag.HtIngressAnnotation, flag.HtIngressTLS,
			flag.HtFnWeight, flag.HtHost, flag.NamespaceFunction, flag.SpecSave, flag.SpecDry,
			flag.HtPrefix, flag.HtKeepPrefix},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "get",
		Aliases: []string{},
		Short:   "Get HTTP trigger details",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.HtName},
		Optional: []flag.Flag{flag.NamespaceTrigger, flag.Output},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "update",
		Aliases: []string{},
		Short:   "Update an HTTP trigger",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.HtName},
		Optional: []flag.Flag{flag.HtUrl, flag.HtFnName,
			flag.HtMethod, flag.HtIngress, flag.HtIngressRule, flag.HtIngressAnnotation,
			flag.HtIngressTLS, flag.HtFnWeight, flag.HtHost, flag.NamespaceTrigger,
			flag.HtPrefix, flag.HtKeepPrefix},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete an HTTP trigger",
	}, Delete, flag.FlagSet{
		Optional: []flag.Flag{flag.HtName, flag.HtFnFilter, flag.NamespaceTrigger, flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List HTTP triggers",
		Long:    "List all HTTP triggers in a namespace if specified, else, list HTTP triggers across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceTrigger, flag.HtFnFilter, flag.AllNamespaces, flag.Output},
	})

	command := &cobra.Command{
		Use:     "httptrigger",
		Aliases: []string{"ht", "route"},
		Short:   "Create, update and manage HTTP triggers",
	}

	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd)

	return command
}
