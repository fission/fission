// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	pkgutil "github.com/fission/fission/pkg/fission-cli/cmd/package/util"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type InfoSubCommand struct {
	cmd.CommandActioner
	name      string
	namespace string
}

func Info(input cli.Input) error {
	return (&InfoSubCommand{}).do(input)
}

func (opts *InfoSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *InfoSubCommand) complete(input cli.Input) (err error) {
	opts.name = input.String(flagkey.PkgName)

	_, opts.namespace, err = opts.GetResourceNamespace(input, flagkey.NamespacePackage)
	if err != nil {
		return fv1.AggregateValidationErrors("Package", err)
	}
	return nil
}

func (opts *InfoSubCommand) run(input cli.Input) error {
	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(opts.namespace).Get(input.Context(), opts.name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error finding package %s: %w", opts.name, err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}
	if handled, err := util.PrintStructured(format, pkg); err != nil || handled {
		return err
	}

	pkgutil.PrintPackageSummary(os.Stdout, pkg)
	return nil
}
