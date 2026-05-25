// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
