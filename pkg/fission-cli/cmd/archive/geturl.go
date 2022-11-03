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
	"net/url"

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

	kubeContext := input.String(flagkey.KubeContext)
	archiveID := input.String(flagkey.ArchiveID)

	serverURL, err := util.GetStorageURL(input.Context(), kubeContext)
	if err != nil {
		return err
	}

	relativeURL, _ := url.Parse(util.FISSION_STORAGE_URI)

	queryString := relativeURL.Query()
	queryString.Set("id", archiveID)
	relativeURL.RawQuery = queryString.Encode()

	storageAccessURL := serverURL.ResolveReference(relativeURL)

	resp, err := http.Head(storageAccessURL.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("error getting URL. Exited with Status:  %s", resp.Status)
	}

	storageType := resp.Header.Get("X-FISSION-STORAGETYPE")

	if storageType == "local" {
		// TODO: testing required here
		storageSvc, err := util.GetSvcName(input.Context(), opts.Client().KubernetesClient, "fission-storage")
		if err != nil {
			return err
		}
		storagesvcURL := "http://" + storageSvc
		client := storagesvcClient.MakeClient(storagesvcURL)
		fmt.Printf("URL: %s", client.GetUrl(archiveID))
	} else if storageType == "s3" {
		storageBucket := resp.Header.Get("X-FISSION-BUCKET")
		s3url := fmt.Sprintf("https://%s.s3.amazonaws.com/%s", storageBucket, archiveID)
		fmt.Printf("URL: %s", s3url)
	}

	return nil
}
