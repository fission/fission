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

package _package

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a package",
		RunE:  wrapper.Wrapper(Create),
	}
	wrapper.SetFlags(createCmd, flag.FlagSet{
		Required: []flag.Flag{flag.PkgEnvironment},
		Optional: []flag.Flag{flag.PkgName, flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure, flag.PkgBuildCmd,
			flag.NamespacePackage, flag.SpecSave, flag.SpecDry},
	})

	getSrcCmd := &cobra.Command{
		Use:   "getsrc",
		Short: "Get package details",
		RunE:  wrapper.Wrapper(GetSrc),
	}
	wrapper.SetFlags(getSrcCmd, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.NamespacePackage, flag.PkgOutput},
	})

	getDeployCmd := &cobra.Command{
		Use:   "getdeploy",
		Short: "Get package details",
		RunE:  wrapper.Wrapper(GetDeploy),
	}
	wrapper.SetFlags(getDeployCmd, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.NamespacePackage, flag.PkgOutput},
	})

	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "Update a package",
		RunE:  wrapper.Wrapper(Update),
	}
	wrapper.SetFlags(updateCmd, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.PkgEnvironment, flag.PkgCode, flag.PkgSrcArchive, flag.PkgDeployArchive,
			flag.PkgSrcChecksum, flag.PkgDeployChecksum, flag.PkgInsecure, flag.PkgBuildCmd, flag.PkgForce,
			flag.NamespacePackage, flag.NamespaceEnvironment},
	})

	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a package",
		RunE:  wrapper.Wrapper(Delete),
	}
	wrapper.SetFlags(deleteCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.PkgName, flag.PkgForce, flag.PkgOrphan, flag.NamespacePackage, flag.IgnoreNotFound},
	})

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List packages",
		Long:  "List all packages in a namespace if specified, else, list packages across all namespaces",
		RunE:  wrapper.Wrapper(List),
	}
	wrapper.SetFlags(listCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.PkgOrphan, flag.PkgStatus, flag.NamespacePackage, flag.AllNamespaces},
	})

	infoCmd := &cobra.Command{
		Use:   "info",
		Short: "Show package information",
		RunE:  wrapper.Wrapper(Info),
	}
	wrapper.SetFlags(infoCmd, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.NamespacePackage},
	})

	rebuildCmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild a failed package",
		RunE:  wrapper.Wrapper(Rebuild),
	}
	wrapper.SetFlags(rebuildCmd, flag.FlagSet{
		Required: []flag.Flag{flag.PkgName},
		Optional: []flag.Flag{flag.NamespacePackage},
	})

	command := &cobra.Command{
		Use:     "package",
		Aliases: []string{"pkg"},
		Short:   "Create, update and manage packages",
	}

	command.AddCommand(createCmd, getSrcCmd, getDeployCmd, updateCmd, deleteCmd, listCmd, infoCmd, rebuildCmd)

	return command
}
