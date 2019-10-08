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
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	cmdutils "github.com/fission/fission/pkg/fission-cli/cmd"
)

type RebuildSubCommand struct {
	client    *client.Client
	name      string
	namespace string
}

func Rebuild(flags cli.Input) error {
	opts := RebuildSubCommand{
		client: cmdutils.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *RebuildSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *RebuildSubCommand) complete(flags cli.Input) error {
	opts.name = flags.String("name")
	if len(opts.name) == 0 {
		return errors.New("Need name of package, use --name")
	}
	opts.namespace = flags.String("pkgNamespace")
	return nil
}

func (opts *RebuildSubCommand) run(flags cli.Input) error {
	pkg, err := opts.client.PackageGet(&metav1.ObjectMeta{
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

	_, err = updatePackageStatus(opts.client, pkg, fv1.BuildStatusPending)
	if err != nil {
		return errors.Wrap(err, "update package status")
	}

	fmt.Printf("Retrying build for pkg %v. Use \"fission pkg info --name %v\" to view status.\n", pkg.Metadata.Name, pkg.Metadata.Name)

	return nil
}
