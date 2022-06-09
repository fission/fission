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
	"fmt"
	"net/http"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type GetURLSubCommand struct {
	cmd.CommandActioner
}

func GetURL(input cli.Input) error {
	return (&GetURLSubCommand{}).do(input)
}

func (opts *GetURLSubCommand) do(input cli.Input) error {

	kubeContext := input.String(flagkey.KubeContext)
	archiveID := input.String(flagkey.ArchiveId)

	storageAccessURL, err := util.GetStorageURL(kubeContext)
	if err != nil {
		return err
	}

	resp, err := http.Head(storageAccessURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Error getting URL. Exited with Status:  %v", resp.Status)
	}

	fmt.Printf("The URL for id: %s is %s", archiveID, fmt.Sprint(resp.Body))

	return nil
}
