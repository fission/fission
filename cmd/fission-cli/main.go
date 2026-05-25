// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"github.com/fission/fission/cmd/fission-cli/app"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
)

func main() {
	cmd := app.App(cmd.ClientOptions{})
	cmd.SilenceErrors = true // use our own error message printer

	err := cmd.Execute()
	if err != nil {
		// let program exit with non-zero code when error occurs
		console.Error(err.Error())
		os.Exit(1)
	}
}
