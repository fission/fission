// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

// Commands returns support commands
func Commands() *cobra.Command {
	dumpCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "dump",
		Short: "Collect & dump all necessary information for troubleshooting",
	}, Dump, flag.FlagSet{
		Optional: []flag.Flag{flag.SupportNoZip, flag.SupportOutput},
	})

	command := &cobra.Command{
		Use:   "support",
		Short: "Collect diagnostic information for support",
	}

	command.AddCommand(dumpCmd)

	return command
}
