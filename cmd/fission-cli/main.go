/*
Copyright 2018 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"os"

	"github.com/fission/fission/cmd/fission-cli/app"
	"github.com/fission/fission/pkg/fission-cli/console"
)

func main() {
	cmd := app.App()
	cmd.SilenceErrors = true // use our own error message printer

	err := cmd.Execute()
	if err != nil {
		// let program exit with non-zero code when error occurs
		console.Error(err.Error())
		os.Exit(1)
	}
}
