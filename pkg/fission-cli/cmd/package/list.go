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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ListSubCommand struct {
	cmd.CommandActioner
	listOrphans  bool
	status       string
	pkgNamespace string
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *ListSubCommand) complete(input cli.Input) (err error) {
	// option for the user to list all orphan packages (not referenced by any function)
	opts.listOrphans = input.Bool(flagkey.PkgOrphan)
	opts.status = input.String(flagkey.PkgStatus)
	_, opts.pkgNamespace, err = util.GetResourceNamespace(input, flagkey.NamespacePackage)
	if err != nil {
		return fv1.AggregateValidationErrors("Environment", err)
	}
	return nil
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {

	var pkgList *fv1.PackageList
	if input.Bool(flagkey.AllNamespaces) {
		pkgList, err = opts.Client().FissionClientSet.CoreV1().Packages(v1.NamespaceAll).List(input.Context(), v1.ListOptions{})
	} else {
		pkgList, err = opts.Client().FissionClientSet.CoreV1().Packages(opts.pkgNamespace).List(input.Context(), v1.ListOptions{})
	}
	if err != nil {
		return err
	}

	// sort the package list by lastUpdatedTimestamp
	sort.Slice(pkgList.Items, func(i, j int) bool {
		return pkgList.Items[i].Status.LastUpdateTimestamp.After(pkgList.Items[j].Status.LastUpdateTimestamp.Time)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", "NAME", "BUILD_STATUS", "ENV", "LASTUPDATEDAT", "NAMESPACE")

	for _, pkg := range pkgList.Items {
		show := true
		// TODO improve list speed when --orphan
		if opts.listOrphans {
			fnList, err := GetFunctionsByPackage(input.Context(), opts.Client(), pkg.ObjectMeta.Name, pkg.ObjectMeta.Namespace)
			if err != nil {
				return errors.Wrap(err, fmt.Sprintf("get functions sharing package %s", pkg.ObjectMeta.Name))
			}
			if len(fnList) > 0 {
				show = false
			}
		}
		if len(opts.status) > 0 && opts.status != string(pkg.Status.BuildStatus) {
			show = false
		}
		if show {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", pkg.ObjectMeta.Name, pkg.Status.BuildStatus, pkg.Spec.Environment.Name, pkg.Status.LastUpdateTimestamp.Format(time.RFC822), pkg.ObjectMeta.Namespace)
		}
	}

	w.Flush()

	return nil
}
