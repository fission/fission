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

	client := storagesvcClient.MakeClient(storageAccessURL.String())
	err = client.Download(input.Context(), archiveID, archiveOutput)
	if err != nil {
		return err
	}

	fmt.Printf("File download complete. File name: %s", archiveOutput)
	return nil
}
