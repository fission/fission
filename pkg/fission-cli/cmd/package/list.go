// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"fmt"
	"sort"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
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
	opts.pkgNamespace, err = opts.ResolveNamespace(input)
	if err != nil {
		return fv1.AggregateValidationErrors("Package", err)
	}
	return nil
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {
	pkgList, err := opts.Client().FissionClientSet.CoreV1().Packages(opts.pkgNamespace).List(input.Context(), v1.ListOptions{})
	if err != nil {
		return err
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	// sort the package list by lastUpdatedTimestamp
	sort.Slice(pkgList.Items, func(i, j int) bool {
		return pkgList.Items[i].Status.LastUpdateTimestamp.After(pkgList.Items[j].Status.LastUpdateTimestamp.Time)
	})

	// apply --orphan / --status filters, then print the surviving packages.
	filtered := make([]fv1.Package, 0, len(pkgList.Items))
	for _, pkg := range pkgList.Items {
		// TODO improve list speed when --orphan
		if opts.listOrphans {
			fnList, err := GetFunctionsByPackage(input.Context(), opts.Client(), pkg.Name, pkg.Namespace)
			if err != nil {
				return fmt.Errorf("get functions sharing package %v: %w", pkg.Name, err)
			}
			if len(fnList) > 0 {
				continue
			}
		}
		if len(opts.status) > 0 && opts.status != string(pkg.Status.BuildStatus) {
			continue
		}
		filtered = append(filtered, pkg)
	}

	headers := []string{"NAME", "BUILD_STATUS", "ENV", "LASTUPDATEDAT", "READY", "NAMESPACE"}
	row := func(pkg fv1.Package) []string {
		return []string{
			pkg.Name, string(pkg.Status.BuildStatus), pkg.Spec.Environment.Name,
			pkg.Status.LastUpdateTimestamp.Format(time.RFC822),
			util.ConditionStatus(pkg.Status.Conditions, fv1.PackageConditionReady),
			pkg.Namespace,
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(pkg fv1.Package) []string { return []string{util.AgeOf(pkg.CreationTimestamp)} }

	return util.PrintObjects(format, filtered, headers, row, wideExtra, wideRow)
}
