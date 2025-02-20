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

package version

import (
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type VersionSubCommand struct {
	cmd.CommandActioner
}

func Version(input cli.Input) error {
	return (&VersionSubCommand{}).do(input)
}

func (opts *VersionSubCommand) do(input cli.Input) error {
	ver := util.GetVersion(input.Context(), input, opts.Client())
	bs, err := yaml.Marshal(ver)
	if err != nil {
		return fmt.Errorf("error formatting versions: %w", err)
	}
	fmt.Print(string(bs))
	return nil
}
