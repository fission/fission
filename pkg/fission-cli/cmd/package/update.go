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
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	pkgName      string
	pkgNamespace string
	force        bool
}

func Update(input cli.Input) error {
	return (&UpdateSubCommand{}).do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateSubCommand) complete(input cli.Input) (err error) {
	opts.pkgName = input.String(flagkey.PkgName)
	_, opts.pkgNamespace, err = util.GetResourceNamespace(input, flagkey.NamespacePackage)
	if err != nil {
		return fv1.AggregateValidationErrors("Environment", err)
	}
	opts.force = input.Bool(flagkey.PkgForce)
	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(opts.pkgNamespace).Get(input.Context(), opts.pkgName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err != nil {
		return errors.Wrap(err, "get package")
	}

	forceUpdate := input.Bool(flagkey.PkgForce)

	fnList, err := GetFunctionsByPackage(input.Context(), opts.Client(), pkg.ObjectMeta.Name, pkg.ObjectMeta.Namespace)
	if err != nil {
		return errors.Wrap(err, "error getting function list")
	}

	if !forceUpdate && len(fnList) > 1 {
		return errors.Errorf("package is used by multiple functions, use --%v to force update", flagkey.PkgForce)
	}

	newPkgMeta, err := UpdatePackage(input, opts.Client(), pkg)
	if err != nil {
		return errors.Wrap(err, "error updating package")
	}

	if pkg.ObjectMeta.ResourceVersion != newPkgMeta.ResourceVersion {
		err = UpdateFunctionPackageResourceVersion(input.Context(), opts.Client(), newPkgMeta, fnList...)
		if err != nil {
			return errors.Wrap(err, "error updating function package reference resource version")
		}
	}

	return nil
}

func UpdatePackage(input cli.Input, client cmd.Client, pkg *fv1.Package) (*metav1.ObjectMeta, error) {
	envName := input.String(flagkey.PkgEnvironment)
	srcArchiveFiles := input.StringSlice(flagkey.PkgSrcArchive)
	deployArchiveFiles := input.StringSlice(flagkey.PkgDeployArchive)
	buildcmd := input.String(flagkey.PkgBuildCmd)
	insecure := input.Bool(flagkey.PkgInsecure)
	deployChecksum := input.String(flagkey.PkgDeployChecksum)
	srcChecksum := input.String(flagkey.PkgSrcChecksum)
	code := input.String(flagkey.PkgCode)

	noZip := false
	needToRebuild := false
	needToUpdate := false

	if input.IsSet(flagkey.PkgCode) {
		deployArchiveFiles = append(deployArchiveFiles, code)
		noZip = true
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgEnvironment) {
		pkg.Spec.Environment.Name = envName
		needToRebuild = true
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgBuildCmd) {
		pkg.Spec.BuildCommand = buildcmd
		needToRebuild = true
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgSrcArchive) {
		srcArchive, err := CreateArchive(client, input, srcArchiveFiles, noZip, insecure, srcChecksum, "", "")
		if err != nil {
			return nil, errors.Wrap(err, "error creating source archive")
		}
		pkg.Spec.Source = *srcArchive
		needToRebuild = true
		needToUpdate = true
	} else if input.IsSet(flagkey.PkgSrcChecksum) {
		pkg.Spec.Source.Checksum = fv1.Checksum{
			Type: fv1.ChecksumTypeSHA256,
			Sum:  srcChecksum,
		}
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgDeployArchive) || input.IsSet(flagkey.PkgCode) {
		deployArchive, err := CreateArchive(client, input, deployArchiveFiles, noZip, insecure, deployChecksum, "", "")
		if err != nil {
			return nil, errors.Wrap(err, "error creating deploy archive")
		}
		pkg.Spec.Deployment = *deployArchive
		// Users may update the env, envNS and deploy archive at the same time,
		// but without the source archive. In this case, we should set needToBuild to false
		needToRebuild = false
		needToUpdate = true
	} else if input.IsSet(flagkey.PkgDeployChecksum) {
		pkg.Spec.Deployment.Checksum = fv1.Checksum{
			Type: fv1.ChecksumTypeSHA256,
			Sum:  deployChecksum,
		}
		needToUpdate = true
	}

	if !needToUpdate {
		return &pkg.ObjectMeta, nil
	}

	// Set package as pending status when needToBuild is true
	if needToRebuild {
		// change into pending state to trigger package build
		pkg.Status = fv1.PackageStatus{
			BuildStatus:         fv1.BuildStatusPending,
			LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
		}
	}

	newPkgMeta, err := client.FissionClientSet.CoreV1().Packages(pkg.ObjectMeta.Namespace).Update(input.Context(), pkg, metav1.UpdateOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "update package")
	}

	fmt.Printf("Package '%v' updated\n", newPkgMeta.GetName())

	return &newPkgMeta.ObjectMeta, err
}

func UpdateFunctionPackageResourceVersion(ctx context.Context, client cmd.Client, pkgMeta *metav1.ObjectMeta, fnList ...fv1.Function) error {
	errs := &multierror.Error{}

	// update resource version of package reference of functions that shared the same package
	for _, fn := range fnList {
		fn.Spec.Package.PackageRef.ResourceVersion = pkgMeta.ResourceVersion
		_, err := client.FissionClientSet.CoreV1().Functions(fn.ObjectMeta.Namespace).Update(ctx, &fn, metav1.UpdateOptions{})
		if err != nil {
			errs = multierror.Append(errs, errors.Wrapf(err, "error updating package resource version of function '%v'", fn.ObjectMeta.Name))
		}
	}

	return errs.ErrorOrNil()
}

func updatePackageStatus(ctx context.Context, client cmd.Client, pkg *fv1.Package, status fv1.BuildStatus) (*metav1.ObjectMeta, error) {
	switch status {
	case fv1.BuildStatusNone, fv1.BuildStatusPending, fv1.BuildStatusRunning, fv1.BuildStatusSucceeded, fv1.CanaryConfigStatusAborted:
		pkg.Status = fv1.PackageStatus{
			BuildStatus:         status,
			LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
		}
		pkg, err := client.FissionClientSet.CoreV1().Packages(pkg.Namespace).Update(ctx, pkg, metav1.UpdateOptions{})
		return &pkg.ObjectMeta, err
	}
	return nil, errors.New("unknown package status")
}
