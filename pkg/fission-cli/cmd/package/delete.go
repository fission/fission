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
	"context"
	"fmt"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
	name          string
	namespace     string
	deleteOrphans bool
	force         bool
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *DeleteSubCommand) complete(input cli.Input) (err error) {
	opts.name = input.String(flagkey.PkgName)
	_, opts.namespace, err = util.GetResourceNamespace(input, flagkey.NamespacePackage)
	if err != nil {
		return fv1.AggregateValidationErrors("Environment", err)
	}

	opts.deleteOrphans = input.Bool(flagkey.PkgOrphan)
	opts.force = input.Bool(flagkey.PkgForce)

	if len(opts.name) == 0 && !opts.deleteOrphans {
		return errors.Errorf("need --%v or --%v flag", flagkey.PkgName, flagkey.PkgOrphan)
	}

	return nil
}

func (opts *DeleteSubCommand) run(input cli.Input) error {
	if len(opts.name) != 0 {
		_, err := opts.Client().FissionClientSet.CoreV1().Packages(opts.namespace).Get(input.Context(), opts.name, metav1.GetOptions{})
		if err != nil {
			if input.Bool(flagkey.IgnoreNotFound) && kerrors.IsNotFound(err) {
				return nil
			}
			return errors.Wrap(err, "find package")
		}

		fnList, err := GetFunctionsByPackage(input.Context(), opts.Client(), opts.name, opts.namespace)
		if err != nil {
			return err
		}

		if !opts.force && len(fnList) > 0 {
			return errors.New("Package is used by at least one function, use -f to force delete")
		}
		err = deletePackage(input.Context(), opts.Client(), opts.name, opts.namespace)
		if err != nil {
			return err
		}
		fmt.Printf("Package '%v' deleted\n", opts.name)
	}

	// TODO improve list speed when --orphan
	if opts.deleteOrphans {
		err := deleteOrphanPkgs(input.Context(), opts.Client(), opts.namespace)
		if err != nil {
			return errors.Wrap(err, "deleting orphan packages")
		}
		fmt.Println("Orphan packages deleted")
	}

	return nil
}

func deleteOrphanPkgs(ctx context.Context, client cmd.Client, pkgNamespace string) error {
	pkgList, err := client.FissionClientSet.CoreV1().Packages(pkgNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	// range through all packages and find out the ones not referenced by any function
	for _, pkg := range pkgList.Items {
		fnList, err := GetFunctionsByPackage(ctx, client, pkg.ObjectMeta.Name, pkgNamespace)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("get functions sharing package %s", pkg.ObjectMeta.Name))
		}
		if len(fnList) == 0 {
			err = deletePackage(ctx, client, pkg.ObjectMeta.Name, pkgNamespace)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func deletePackage(ctx context.Context, client cmd.Client, pkgName string, pkgNamespace string) error {
	return client.FissionClientSet.CoreV1().Packages(pkgNamespace).Delete(ctx, pkgName, metav1.DeleteOptions{})
}
