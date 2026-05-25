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

type DeleteSubCommand struct {
	cmd.CommandActioner
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {

	archiveID := input.String(flagkey.ArchiveID)

	storagesvcURL, err := util.GetStorageURL(input.Context(), opts.Client())
	if err != nil {
		return err
	}

	hmacSecret, err := storagesvcClient.HMACSecretFromCluster(input.Context(), opts.Client().KubernetesClient, util.GetFissionNamespace())
	if err != nil {
		return err
	}

	client := storagesvcClient.MakeClient(storagesvcURL.String(), hmacSecret)

	err = client.Delete(input.Context(), archiveID)
	if err != nil {
		return err
	}

	fmt.Printf("Deleted archive with id: %s\n", archiveID)

	return nil
}
