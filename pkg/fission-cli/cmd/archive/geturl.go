// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"fmt"
	"net/http"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
)

type GetURLSubCommand struct {
	cmd.CommandActioner
}

func GetURL(input cli.Input) error {
	return (&GetURLSubCommand{}).do(input)
}

func (opts *GetURLSubCommand) do(input cli.Input) error {

	archiveID := input.String(flagkey.ArchiveID)

	serverURL, err := util.GetStorageURL(input.Context(), opts.Client())
	if err != nil {
		return err
	}

	hmacSecret, err := storagesvcClient.HMACSecretFromCluster(input.Context(), opts.Client().KubernetesClient, util.GetFissionNamespace())
	if err != nil {
		return err
	}

	client := storagesvcClient.MakeClient(serverURL.String(), hmacSecret)

	resp, err := client.Info(input.Context(), archiveID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("error getting URL. Exited with Status:  %s", resp.Status)
	}

	storageType := resp.Header.Get("X-FISSION-STORAGETYPE")

	switch storageType {
	case "local":
		fmt.Printf("URL: %s", client.GetUrl(archiveID))
	case "s3":
		storageBucket := resp.Header.Get("X-FISSION-BUCKET")
		s3url := fmt.Sprintf("https://%s.s3.amazonaws.com/%s", storageBucket, archiveID)
		fmt.Printf("URL: %s", s3url)
	}

	return nil
}
