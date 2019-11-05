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

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	client             *client.Client
	pkgName            string
	pkgNamespace       string
	force              bool
	envName            string
	envNamespace       string
	srcArchiveFiles    []string
	deployArchiveFiles []string
	buildcmd           string
	keepURL            bool
}

func Update(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := UpdateSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *UpdateSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *UpdateSubCommand) complete(flags cli.Input) error {
	opts.pkgName = flags.String("name")
	if len(opts.pkgName) == 0 {
		return errors.New("Need --name argument.")
	}
	opts.pkgNamespace = flags.String("pkgNamespace")
	opts.force = flags.Bool("f")
	opts.envName = flags.String("env")
	opts.envNamespace = flags.String("envNamespace")
	opts.srcArchiveFiles = flags.StringSlice("src")
	opts.deployArchiveFiles = flags.StringSlice("deploy")
	opts.buildcmd = flags.String("buildcmd")
	opts.keepURL = flags.Bool("keepurl")

	if len(opts.srcArchiveFiles) > 0 && len(opts.deployArchiveFiles) > 0 {
		return errors.New("Need either of --src or --deploy and not both arguments.")
	}

	if len(opts.srcArchiveFiles) == 0 && len(opts.deployArchiveFiles) == 0 &&
		len(opts.envName) == 0 && len(opts.buildcmd) == 0 {
		return errors.New("Need --env or --src or --deploy or --buildcmd argument.")
	}
	return nil
}

func (opts *UpdateSubCommand) run(flags cli.Input) error {
	pkg, err := opts.client.PackageGet(&metav1.ObjectMeta{
		Namespace: opts.pkgNamespace,
		Name:      opts.pkgName,
	})
	if err != nil {
		return errors.Wrap(err, "get package")
	}

	// if the new env specified is the same as the old one, no need to update package
	// same is true for all update parameters, but, for now, we dont check all of them - because, its ok to
	// re-write the object with same old values, we just end up getting a new resource version for the object.
	if len(opts.envName) > 0 && opts.envName == pkg.Spec.Environment.Name {
		opts.envName = ""
	}

	if opts.envNamespace == pkg.Spec.Environment.Namespace {
		opts.envNamespace = ""
	}

	fnList, err := GetFunctionsByPackage(opts.client, pkg.Metadata.Name, pkg.Metadata.Namespace)
	if err != nil {
		return errors.Wrap(err, "get function list")
	}

	if !opts.force && len(fnList) > 1 {
		return errors.New("Package is used by multiple functions, use --force to force update")
	}

	newPkgMeta, err := UpdatePackage(opts.client, pkg,
		opts.envName, opts.envNamespace, opts.srcArchiveFiles,
		opts.deployArchiveFiles, opts.buildcmd, false, false, opts.keepURL)
	if err != nil {
		return errors.Wrap(err, "update package")
	}

	// update resource version of package reference of functions that shared the same package
	for _, fn := range fnList {
		fn.Spec.Package.PackageRef.ResourceVersion = newPkgMeta.ResourceVersion
		_, err := opts.client.FunctionUpdate(&fn)
		if err != nil {
			return errors.Wrap(err, "update function")
		}
	}

	fmt.Printf("Package '%v' updated\n", newPkgMeta.GetName())
	return nil
}

func UpdatePackage(client *client.Client, pkg *fv1.Package, envName, envNamespace string,
	srcArchiveFiles []string, deployArchiveFiles []string, buildcmd string, forceRebuild bool, noZip bool, keepURL bool) (*metav1.ObjectMeta, error) {

	needToBuild := false

	if len(envName) > 0 {
		pkg.Spec.Environment.Name = envName
		needToBuild = true
	}

	if len(envNamespace) > 0 {
		pkg.Spec.Environment.Namespace = envNamespace
		needToBuild = true
	}

	if len(buildcmd) > 0 {
		pkg.Spec.BuildCommand = buildcmd
		needToBuild = true
	}

	if len(srcArchiveFiles) > 0 {
		srcArchive, err := CreateArchive(client, srcArchiveFiles, false, keepURL, "", "")
		if err != nil {
			return nil, err
		}
		pkg.Spec.Source = *srcArchive
		needToBuild = true
	}

	if len(deployArchiveFiles) > 0 {
		deployArchive, err := CreateArchive(client, deployArchiveFiles, noZip, keepURL, "", "")
		if err != nil {
			return nil, err
		}
		pkg.Spec.Deployment = *deployArchive
		// Users may update the env, envNS and deploy archive at the same time,
		// but without the source archive. In this case, we should set needToBuild to false
		needToBuild = false
	}

	// Set package as pending status when needToBuild is true
	if needToBuild || forceRebuild {
		// change into pending state to trigger package build
		pkg.Status = fv1.PackageStatus{
			BuildStatus:         fv1.BuildStatusPending,
			LastUpdateTimestamp: time.Now().UTC(),
		}
	}

	newPkgMeta, err := client.PackageUpdate(pkg)
	if err != nil {
		return nil, errors.Wrap(err, "update package")
	}

	return newPkgMeta, err
}

func updatePackageStatus(client *client.Client, pkg *fv1.Package, status fv1.BuildStatus) (*metav1.ObjectMeta, error) {
	switch status {
	case fv1.BuildStatusNone, fv1.BuildStatusPending, fv1.BuildStatusRunning, fv1.BuildStatusSucceeded, fv1.CanaryConfigStatusAborted:
		pkg.Status = fv1.PackageStatus{
			BuildStatus:         status,
			LastUpdateTimestamp: time.Now().UTC(),
		}
		pkg, err := client.PackageUpdate(pkg)
		return pkg, err
	}
	return nil, errors.New("unknown package status")
}
