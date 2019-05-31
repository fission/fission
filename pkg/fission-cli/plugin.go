/*
Copyright 2016 The Fission Authors.

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

package fission_cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli"

	"github.com/fission/fission/pkg/fission-cli/plugin"
)

var cmdPlugin = cli.Command{
	Name:    "plugin",
	Aliases: []string{"plugins"},
	Usage:   "Manage Fission CLI plugins",
	Subcommands: []cli.Command{
		{
			Name:   "list",
			Usage:  "List installed client plugins",
			Action: pluginList,
		},
	},
}

func pluginList(_ *cli.Context) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintln(w, "NAME\tVERSION\tPATH")
	for _, p := range plugin.FindAll() {
		fmt.Fprintf(w, "%v\t%v\t%v\n", p.Name, p.Version, p.Path)
	}
	w.Flush()
	return nil
}
