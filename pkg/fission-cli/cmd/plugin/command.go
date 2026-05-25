// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
)

func Commands() *cobra.Command {
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List installed client plugins",
		RunE:  wrapper.Wrapper(List),
	}

	command := &cobra.Command{
		Use:     "plugin",
		Aliases: []string{"plugins"},
		Short:   "Manage CLI plugins",
		Hidden:  true,
	}

	command.AddCommand(listCmd)

	return command
}
