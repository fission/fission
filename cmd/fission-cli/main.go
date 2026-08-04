// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"github.com/fission/fission/cmd/fission-cli/app"
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
)

func main() {
	// Same fail-closed contract as the server binaries: an exported-but-empty
	// FISSION_INTERNAL_AUTH_SECRET would make every signed CLI call silently
	// fall back to unsigned (and 401 against a verifying cluster) — refuse it
	// with a message that names the variable instead.
	if err := fv1.ValidateInternalAuthEnv(); err != nil {
		console.Error(err.Error())
		os.Exit(1)
	}

	cmd := app.App(cmd.ClientOptions{})
	cmd.SilenceErrors = true // use our own error message printer

	err := cmd.Execute()
	if err != nil {
		// let program exit with non-zero code when error occurs
		console.Error(err.Error())
		os.Exit(1)
	}
}
