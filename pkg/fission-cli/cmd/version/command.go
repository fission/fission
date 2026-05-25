// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package version

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	command := wrapper.SubCommand(&cobra.Command{
		Use:   "version",
		Short: "Show client/server version information",
	}, Version, flag.FlagSet{
		Optional: []flag.Flag{flag.ClientOnly},
	})

	return command
}
