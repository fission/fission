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

	client := storagesvcClient.MakeClient(storagesvcURL.String())
	archiveID, err := client.Upload(input.Context(), archiveName, nil)
	if err != nil {
		return err
	}

	fmt.Fprintf(input.Stdout(), "File successfully uploaded with ID: %s\n", archiveID)
	return nil
}
