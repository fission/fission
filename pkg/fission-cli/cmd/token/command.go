/*
Copyright 2022 The Fission Authors.

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

package token

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a JWT token for function invocation",
		RunE:  wrapper.Wrapper(Create),
	}
	wrapper.SetFlags(createCmd, flag.FlagSet{
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
