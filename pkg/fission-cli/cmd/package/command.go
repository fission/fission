// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a package",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.PkgEnvironment},
		Optional: []flag.Flag{flag.PkgName, flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure, flag.PkgOCI, flag.PkgBuildCmd,
			flag.NamespacePackage, flag.SpecSave, flag.SpecDry},
	})

	getSrcCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "getsrc",
		Short: "Get package details",
	}, GetSrc, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.NamespacePackage, flag.PkgOutput},
	})

	getDeployCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "getdeploy",
		Short: "Get package details",
	}, GetDeploy, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.NamespacePackage, flag.PkgOutput},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "update",
		Short: "Update a package",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.PkgEnvironment, flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure, flag.PkgOCI, flag.PkgBuildCmd, flag.PkgForce,
			flag.NamespacePackage, flag.NamespaceEnvironment},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "delete",
		Short: "Delete a package",
	}, Delete, flag.FlagSet{
		Optional: []flag.Flag{flag.PkgName, flag.PkgForce, flag.PkgOrphan, flag.NamespacePackage, flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List packages",
		Long:  "List all packages in a namespace if specified, else, list packages across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.PkgOrphan, flag.PkgStatus, flag.NamespacePackage, flag.AllNamespaces, flag.Output},
	})

	infoCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "info",
		Short: "Show package information",
	}, Info, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.NamespacePackage, flag.Output},
	})

	rebuildCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild a failed package",
	}, Rebuild, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.NamespacePackage},
	})

	command := &cobra.Command{
		Use:     "package",
		Aliases: []string{"pkg"},
		Short:   "Create, update and manage packages",
	}

	waitCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "wait",
		Short: "Wait for a package to reach a status condition",
	}, Wait, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName, flag.WaitFor},
		Optional: []flag.Flag{flag.NamespacePackage, flag.WaitTimeout},
	})

	command.AddCommand(createCmd, getSrcCmd, getDeployCmd, updateCmd, deleteCmd, listCmd, infoCmd, rebuildCmd, waitCmd)

	return command
}
