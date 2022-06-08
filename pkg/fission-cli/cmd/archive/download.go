/*
Copyright 2019 The Fission Authors.

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
	"io"
	"net/http"
	"os"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DownloadSubCommand struct {
	cmd.CommandActioner
}

func Download(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *DownloadSubCommand) do(input cli.Input) error {

	kubeContext := input.String(flagkey.KubeContext)
	archiveID := input.String(flagkey.ArchiveId)

	storageAccessURL, err := util.GetStorageURL(kubeContext, archiveID)
	if err != nil {
		return err
	}

	resp, err := http.Get(storageAccessURL)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	out, err := os.Create("downloaded.zip")
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}
