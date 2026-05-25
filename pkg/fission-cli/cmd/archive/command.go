// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
