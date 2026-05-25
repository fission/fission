// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/plugin"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintln(w, "NAME\tVERSION\tPATH")
	for _, p := range plugin.FindAll(input.Context()) {
		fmt.Fprintf(w, "%v\t%v\t%v\n", p.Name, p.Version, p.Path)
	}
	w.Flush()
	return nil
}
