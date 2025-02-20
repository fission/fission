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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	pkgutil "github.com/fission/fission/pkg/fission-cli/cmd/package/util"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
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
		return fv1.AggregateValidationErrors("Environment", err)
	}
	return nil
}

func (opts *InfoSubCommand) run(input cli.Input) error {
	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(opts.namespace).Get(input.Context(), opts.name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error finding package %s: %w", opts.name, err)
	}

	pkgutil.PrintPackageSummary(os.Stdout, pkg)
	return nil
}
