// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"fmt"
	"strings"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
)

type DownloadSubCommand struct {
	cmd.CommandActioner
}

func Download(input cli.Input) error {
	return (&DownloadSubCommand{}).do(input)
}

func (opts *DownloadSubCommand) do(input cli.Input) error {

	archiveID := input.String(flagkey.ArchiveID)
	archiveOutput := input.String(flagkey.ArchiveOutput)

	if len(archiveOutput) == 0 {
		archiveOutput = strings.TrimPrefix(archiveID, "/fission/fission-functions/")
	}

	storageAccessURL, err := util.GetStorageURL(input.Context(), opts.Client())
	if err != nil {
		return err
	}

	hmacSecret, err := storagesvcClient.HMACSecretFromCluster(input.Context(), opts.Client().KubernetesClient, util.GetFissionNamespace())
	if err != nil {
		return err
	}

	client := storagesvcClient.MakeClient(storageAccessURL.String(), hmacSecret)
	err = client.Download(input.Context(), archiveID, archiveOutput)
	if err != nil {
		return err
	}

	fmt.Printf("File download complete. File name: %s", archiveOutput)
	return nil
}
