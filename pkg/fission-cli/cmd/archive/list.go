// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"fmt"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {

	storageAccessURL, err := util.GetStorageURL(input.Context(), opts.Client())
	if err != nil {
		return err
	}

	hmacSecret, err := storagesvcClient.HMACSecretFromCluster(input.Context(), opts.Client().KubernetesClient, util.GetFissionNamespace())
	if err != nil {
		return err
	}

	client := storagesvcClient.MakeClient(storageAccessURL.String(), hmacSecret)
	files, err := client.List(input.Context())
	if err != nil {
		return err
	}

	fmt.Println("ARCHIVES")
	for _, file := range files {
		fmt.Println(file)
	}

	return nil

}
