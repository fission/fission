// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package check

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	command := wrapper.SubCommand(&cobra.Command{
		Use:   "check",
		Short: "Check the fission installation for potential problems",
		Long:  `Check the fission installation for potential problems.`,
	}, Check, flag.FlagSet{
		Optional: []flag.Flag{flag.PreCheckOnly},
	})

	return command
}
