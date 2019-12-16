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

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type RebuildSubCommand struct {
	cmd.CommandActioner
	name      string
	namespace string
}

func Rebuild(input cli.Input) error {
	return (&RebuildSubCommand{}).do(input)
}

func (opts *RebuildSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *RebuildSubCommand) complete(input cli.Input) error {
	opts.name = input.String(flagkey.PkgName)
	opts.namespace = input.String(flagkey.NamespacePackage)
	return nil
}

func (opts *RebuildSubCommand) run(input cli.Input) error {
	pkg, err := opts.Client().V1().Package().Get(&metav1.ObjectMeta{
		Name:      opts.name,
		Namespace: opts.namespace,
	})
	if err != nil {
		return errors.Wrap(err, "find package")
	}

	if pkg.Status.BuildStatus != fv1.BuildStatusFailed {
		return errors.New(fmt.Sprintf("Package %v is not in %v state.",
			pkg.Metadata.Name, fv1.BuildStatusFailed))
	}

	_, err = updatePackageStatus(opts.Client(), pkg, fv1.BuildStatusPending)
	if err != nil {
		return errors.Wrap(err, "update package status")
	}

	fmt.Printf("Retrying build for pkg %v. Use \"fission pkg info --name %v\" to view status.\n", pkg.Metadata.Name, pkg.Metadata.Name)

	return nil
}
