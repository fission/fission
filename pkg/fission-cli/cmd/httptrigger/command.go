// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
			flag.HtRouteProvider, flag.HtRouteHost, flag.HtRoutePath, flag.HtRouteAnnotation,
			flag.HtRouteTLS, flag.HtGateway,
			flag.HtFnWeight, flag.HtHost, flag.SpecSave, flag.SpecDry,
			flag.HtPrefix, flag.HtKeepPrefix, flag.HtInvocationMode},
	})

	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "get",
		Aliases: []string{},
		Short:   "Get HTTP trigger details",
	}, Get, flag.FlagSet{
		Required: []flag.Flag{flag.HtName},
		Optional: []flag.Flag{flag.Output},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "update",
		Aliases: []string{},
		Short:   "Update an HTTP trigger",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.HtName},
		Optional: []flag.Flag{flag.HtUrl, flag.HtFnName,
			flag.HtMethod, flag.HtIngress, flag.HtIngressRule, flag.HtIngressAnnotation,
			flag.HtIngressTLS, flag.HtRouteProvider, flag.HtRouteHost, flag.HtRoutePath,
			flag.HtRouteAnnotation, flag.HtRouteTLS, flag.HtGateway,
			flag.HtFnWeight, flag.HtHost, flag.HtPrefix, flag.HtKeepPrefix, flag.HtInvocationMode},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete an HTTP trigger",
	}, Delete, flag.FlagSet{
		Optional: []flag.Flag{flag.HtName, flag.HtFnFilter, flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List HTTP triggers",
		Long:    "List all HTTP triggers in a namespace if specified, else, list HTTP triggers across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.HtFnFilter, flag.AllNamespaces, flag.Output},
	})

	command := &cobra.Command{
		Use:     "httptrigger",
		Aliases: []string{"ht", "route"},
		Short:   "Create, update and manage HTTP triggers",
	}

	waitCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "wait",
		Short: "Wait for an HTTP trigger to reach a status condition",
	}, Wait, flag.FlagSet{
		Required: []flag.Flag{flag.HtName, flag.WaitFor},
		Optional: []flag.Flag{flag.WaitTimeout},
	})

	command.AddCommand(createCmd, getCmd, updateCmd, deleteCmd, listCmd, waitCmd)

	return command
}
