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
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
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

func (opts *UpdateSubCommand) complete(input cli.Input) error {
	opts.pkgName = input.String(flagkey.PkgName)
	opts.pkgNamespace = input.String(flagkey.NamespacePackage)
	opts.force = input.Bool(flagkey.PkgForce)
	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	pkg, err := opts.Client().V1().Package().Get(&metav1.ObjectMeta{
		Namespace: opts.pkgNamespace,
		Name:      opts.pkgName,
	})
	if err != nil {
		return errors.Wrap(err, "get package")
	}

	forceUpdate := input.Bool(flagkey.PkgForce)

	fnList, err := GetFunctionsByPackage(opts.Client(), pkg.Metadata.Name, pkg.Metadata.Namespace)
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

	if pkg.Metadata.ResourceVersion != newPkgMeta.ResourceVersion {
		err = UpdateFunctionPackageResourceVersion(opts.Client(), newPkgMeta, fnList...)
		if err != nil {
			return errors.Wrap(err, "error updating function package reference resource version")
		}
	}

	return nil
}

func UpdatePackage(input cli.Input, client client.Interface, pkg *fv1.Package) (*metav1.ObjectMeta, error) {
	envName := input.String(flagkey.PkgEnvironment)
	envNamespace := input.String(flagkey.NamespaceEnvironment)
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

	if input.IsSet(flagkey.NamespaceEnvironment) {
		pkg.Spec.Environment.Namespace = envNamespace
		needToRebuild = true
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgBuildCmd) {
		pkg.Spec.BuildCommand = buildcmd
		needToRebuild = true
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgSrcArchive) {
		srcArchive, err := CreateArchive(client, srcArchiveFiles, noZip, insecure, srcChecksum, "", "")
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
		deployArchive, err := CreateArchive(client, deployArchiveFiles, noZip, insecure, deployChecksum, "", "")
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
			Sum:  srcChecksum,
		}
		needToUpdate = true
	}

	if !needToUpdate {
		return &pkg.Metadata, nil
	}

	// Set package as pending status when needToBuild is true
	if needToRebuild {
		// change into pending state to trigger package build
		pkg.Status = fv1.PackageStatus{
			BuildStatus:         fv1.BuildStatusPending,
			LastUpdateTimestamp: time.Now().UTC(),
		}
	}

	newPkgMeta, err := client.V1().Package().Update(pkg)
	if err != nil {
		return nil, errors.Wrap(err, "update package")
	}

	fmt.Printf("Package '%v' updated\n", newPkgMeta.GetName())

	return newPkgMeta, err
}

func UpdateFunctionPackageResourceVersion(client client.Interface, pkgMeta *metav1.ObjectMeta, fnList ...fv1.Function) error {
	errs := &multierror.Error{}

	// update resource version of package reference of functions that shared the same package
	for _, fn := range fnList {
		fn.Spec.Package.PackageRef.ResourceVersion = pkgMeta.ResourceVersion
		_, err := client.V1().Function().Update(&fn)
		if err != nil {
			errs = multierror.Append(errs, errors.Wrapf(err, "error updating package resource version of function '%v'", fn.Metadata.Name))
		}
	}

	return errs.ErrorOrNil()
}

func updatePackageStatus(client client.Interface, pkg *fv1.Package, status fv1.BuildStatus) (*metav1.ObjectMeta, error) {
	switch status {
	case fv1.BuildStatusNone, fv1.BuildStatusPending, fv1.BuildStatusRunning, fv1.BuildStatusSucceeded, fv1.CanaryConfigStatusAborted:
		pkg.Status = fv1.PackageStatus{
			BuildStatus:         status,
			LastUpdateTimestamp: time.Now().UTC(),
		}
		pkg, err := client.V1().Package().Update(pkg)
		return pkg, err
	}
	return nil, errors.New("unknown package status")
}
