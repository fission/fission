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
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type PutSubCommand struct {
	cmd.CommandActioner
}

func Put(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *PutSubCommand) do(input cli.Input) error {

	kubeContext := input.String(flagkey.KubeContext)
	archiveName := input.String(flagkey.ArchiveName)

	client := &http.Client{}

	storageAccessURL, err := util.GetStorageURL(kubeContext, "")
	if err != nil {
		return err
	}

	data, err := os.ReadFile(archiveName)
	if err != nil {
		return err
	}

	request, err := http.NewRequest("POST", storageAccessURL, bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "multipart/form-data")
	request.Header.Set("X-File-Size", fmt.Sprint(len(data)))

	resp, err := client.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	fmt.Println(body)

	return nil
}
