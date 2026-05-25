// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"fmt"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
)

type UploadSubCommand struct {
	cmd.CommandActioner
}

func Upload(input cli.Input) error {
	return (&UploadSubCommand{}).do(input)
}

func (opts *UploadSubCommand) do(input cli.Input) error {

	archiveName := input.String(flagkey.ArchiveName)

	storagesvcURL, err := util.GetStorageURL(input.Context(), opts.Client())
	if err != nil {
		return err
	}

	hmacSecret, err := storagesvcClient.HMACSecretFromCluster(input.Context(), opts.Client().KubernetesClient, util.GetFissionNamespace())
	if err != nil {
		return err
	}

	client := storagesvcClient.MakeClient(storagesvcURL.String(), hmacSecret)
	archiveID, err := client.Upload(input.Context(), archiveName, nil)
	if err != nil {
		return err
	}

	fmt.Fprintf(input.Stdout(), "File successfully uploaded with ID: %s\n", archiveID)
	return nil
}
