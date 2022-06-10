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

	uploadCmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload an archive",
		RunE:  wrapper.Wrapper(Upload),
	}
	wrapper.SetFlags(uploadCmd, flag.FlagSet{
		Required: []flag.Flag{flag.ArchiveName},
		Optional: []flag.Flag{flag.KubeContext},
	})

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all uploaded archives",
		RunE:  wrapper.Wrapper(List),
	}
	wrapper.SetFlags(listCmd, flag.FlagSet{
		Optional: []flag.Flag{flag.KubeContext},
	})

	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete an archive",
		RunE:  wrapper.Wrapper(Delete),
	}
	wrapper.SetFlags(deleteCmd, flag.FlagSet{
		Required: []flag.Flag{flag.ArchiveId},
		Optional: []flag.Flag{flag.ArchiveOutput},
	})

	geturlCmd := &cobra.Command{
		Use:   "get-url",
		Short: "Get url of a uploaded archive",
		RunE:  wrapper.Wrapper(GetURL),
	}
	wrapper.SetFlags(geturlCmd, flag.FlagSet{
		Required: []flag.Flag{flag.ArchiveId},
		Optional: []flag.Flag{flag.KubeContext},
	})

	downloadCmd := &cobra.Command{
		Use:   "download",
		Short: "Download an archive",
		RunE:  wrapper.Wrapper(Download),
	}
	wrapper.SetFlags(downloadCmd, flag.FlagSet{
		Required: []flag.Flag{flag.ArchiveId},
		Optional: []flag.Flag{flag.KubeContext, flag.ArchiveOutput},
	})

	command := &cobra.Command{
		Use:     "archive",
		Short:   "For managing archives",
		Aliases: []string{"ar"},
	}

	command.AddCommand(uploadCmd, listCmd, deleteCmd, geturlCmd, downloadCmd)
	return command
}
