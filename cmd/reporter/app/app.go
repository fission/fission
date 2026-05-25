// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"
)

func App() *cobra.Command {
	cobra.EnableCommandSorting = false
	rootCmd := &cobra.Command{
		Use:       "reporter",
		Short:     "Report fission events to analytics service",
		ValidArgs: []string{"event"},
		Args:      cobra.OnlyValidArgs,
	}
	rootCmd.AddCommand(EventCommand())
	return rootCmd
}
