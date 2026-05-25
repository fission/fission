// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package token

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a JWT token for function invocation",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.TokUsername, flag.TokPassword},
		Optional: []flag.Flag{flag.TokAuthURI},
	})

	command := &cobra.Command{
		Use:   "token",
		Short: "Create a JWT token for function invocation",
	}

	command.AddCommand(createCmd)

	return command
}
