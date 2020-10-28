/*
Copyright 2019 The Fission Authors.

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

package support

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

//Commands returns support commands
func Commands() *cobra.Command {
	dumpCmd := &cobra.Command{
		Use:   "dump",
		Short: "Collect & dump all necessary information for troubleshooting",
		RunE:  wrapper.Wrapper(Dump),
	}
	wrapper.SetFlags(dumpCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.SupportNoZip, flag.SupportOutput},
	})

	command := &cobra.Command{
		Use:   "support",
		Short: "Collect diagnostic information for support",
	}

	command.AddCommand(dumpCmd)

	return command
}
