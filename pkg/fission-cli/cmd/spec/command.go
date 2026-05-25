// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	initCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "init",
		Short: "Create an initial declarative application specification",
	}, Init, flag.FlagSet{
		Optional: []flag.Flag{flag.SpecName, flag.SpecDeployID, flag.SpecDir},
	})

	validateCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate declarative application specification",
	}, Validate, flag.FlagSet{
		Optional: []flag.Flag{flag.SpecDir, flag.SpecIgnore, flag.SpecAllowConflicts},
	})

	applyCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "apply",
		Short: "Create, update, or delete resources from application specification",
	}, Apply, flag.FlagSet{
		Optional: []flag.Flag{flag.SpecDir, flag.SpecIgnore, flag.SpecDelete, flag.SpecWait, flag.SpecWatch,
			flag.SpecValidation, flag.SpecApplyCommitLabel, flag.SpecAllowConflicts, flag.ForceNamespace},
	})

	destroyCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "destroy",
		Short: "Delete all Fission resources in the application specification",
	}, Destroy, flag.FlagSet{
		Optional: []flag.Flag{flag.SpecDir, flag.SpecIgnore, flag.ForceDelete},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List all the resources that were created through this spec",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.SpecDeployID, flag.SpecDir, flag.SpecIgnore, flag.AllNamespaces},
	})

	command := &cobra.Command{
		Use:     "spec",
		Aliases: []string{"specs"},
		Short:   "Manage a declarative application specification",
	}

	command.AddCommand(initCmd, validateCmd, applyCmd, listCmd, destroyCmd)

	return command
}
