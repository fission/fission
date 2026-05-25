// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/fission/fission/cmd/reporter/app"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func main() {
	logger := loggerfactory.GetLogger()

	err := app.App().Execute()
	if err != nil {
		logger.Error(err, "error occurred during analytics reporting")
	}
}
