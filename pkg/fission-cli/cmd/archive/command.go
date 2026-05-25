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

package archive

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {

	uploadCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "upload",
		Short: "Upload an archive",
	}, Upload, flag.FlagSet{
		Required: []flag.Flag{flag.ArchiveName},
		Optional: []flag.Flag{},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List all uploaded archives",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "delete",
		Short: "Delete an archive",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.ArchiveID},
		Optional: []flag.Flag{flag.ArchiveOutput},
	})

	geturlCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "get-url",
		Short: "Get URL of an uploaded archive",
	}, GetURL, flag.FlagSet{
		Required: []flag.Flag{flag.ArchiveID},
		Optional: []flag.Flag{},
	})

	downloadCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "download",
		Short: "Download an archive",
	}, Download, flag.FlagSet{
		Required: []flag.Flag{flag.ArchiveID},
		Optional: []flag.Flag{flag.ArchiveOutput},
	})

	command := &cobra.Command{
		Use:     "archive",
		Short:   "Manage archives stored with Fission Storage Service.",
		Aliases: []string{"ar"},
	}

	command.AddCommand(uploadCmd, listCmd, deleteCmd, geturlCmd, downloadCmd)
	return command
}
