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

package _package

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	client       *client.Client
	listOrphans  bool
	status       string
	pkgNamespace string
}

func List(input cli.Input) error {
	c, err := util.GetServer(input)
	if err != nil {
		return err
	}
	opts := ListSubCommand{
		client: c,
	}
	return opts.do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *ListSubCommand) complete(input cli.Input) error {
	// option for the user to list all orphan packages (not referenced by any function)
	opts.listOrphans = input.Bool(flagkey.PkgOrphan)
	opts.status = input.String(flagkey.PkgStatus)
	opts.pkgNamespace = input.String(flagkey.NamespacePackage)
	return nil
}

func (opts *ListSubCommand) run(input cli.Input) error {
	pkgList, err := opts.client.PackageList(opts.pkgNamespace)
	if err != nil {
		return err
	}

	// sort the package list by lastUpdatedTimestamp
	sort.Slice(pkgList, func(i, j int) bool {
		return pkgList[i].Status.LastUpdateTimestamp.After(pkgList[j].Status.LastUpdateTimestamp)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", "NAME", "BUILD_STATUS", "ENV", "LASTUPDATEDAT")

	for _, pkg := range pkgList {
		show := true
		// TODO improve list speed when --orphan
		if opts.listOrphans {
			fnList, err := GetFunctionsByPackage(opts.client, pkg.Metadata.Name, pkg.Metadata.Namespace)
			if err != nil {
				return errors.Wrap(err, fmt.Sprintf("get functions sharing package %s", pkg.Metadata.Name))
			}
			if len(fnList) > 0 {
				show = false
			}
		}
		if len(opts.status) > 0 && opts.status != string(pkg.Status.BuildStatus) {
			show = false
		}
		if show {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", pkg.Metadata.Name, pkg.Status.BuildStatus, pkg.Spec.Environment.Name, pkg.Status.LastUpdateTimestamp.Format(time.RFC822))
		}
	}

	w.Flush()

	return nil
}
