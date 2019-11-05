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

package canaryconfig

import (
	"fmt"

	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DeleteSubCommand struct {
	client *client.Client
}

func Delete(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := DeleteSubCommand{
		client: c,
	}
	return opts.run(flags)
}

func (opts *DeleteSubCommand) run(flags cli.Input) error {
	metadata, err := util.GetMetadata("name", "canaryNamespace", flags)
	if err != nil {
		return err
	}
	err = opts.client.CanaryConfigDelete(metadata)
	if err != nil {
		return errors.Wrap(err, "error deleting canary config")
	}

	fmt.Printf("canaryconfig '%v.%v' deleted\n", metadata.Name, metadata.Namespace)
	return nil
}
