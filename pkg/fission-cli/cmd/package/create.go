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
	"path"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	client *client.Client
}

func Create(input cli.Input) error {
	c, err := util.GetServer(input)
	if err != nil {
		return err
	}
	opts := CreateSubCommand{
		client: c,
	}
	return opts.do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.run(input)
	if err != nil {
		return err
	}
	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	pkgName := input.String(flagkey.PkgName)
	if len(pkgName) == 0 {
		console.Warn(fmt.Sprintf("--%v will be soon marked as required flag, see 'help' for details", flagkey.HtName))
	}
	pkgNamespace := input.String(flagkey.NamespacePackage)
	envName := input.String(flagkey.PkgEnvironment)
	envNamespace := input.String(flagkey.NamespaceEnvironment)
	srcArchiveFiles := input.StringSlice(flagkey.PkgSrcArchive)
	deployArchiveFiles := input.StringSlice(flagkey.PkgDeployArchive)
	buildcmd := input.String(flagkey.PkgBuildCmd)
	keepURL := input.Bool(flagkey.PkgKeepURL)

	if len(srcArchiveFiles) == 0 && len(deployArchiveFiles) == 0 {
		return errors.Errorf("need --%v or --%v flag", flagkey.PkgSrcArchive, flagkey.PkgDeployArchive)
	}

	_, err := CreatePackage(input, opts.client, pkgName, pkgNamespace, envName, envNamespace,
		srcArchiveFiles, deployArchiveFiles, buildcmd, "", "", false, keepURL)

	return err
}

// TODO: get all necessary value from CLI input directly
func CreatePackage(input cli.Input, client *client.Client, pkgName string, pkgNamespace string, envName string, envNamespace string,
	srcArchiveFiles []string, deployArchiveFiles []string, buildcmd string, specDir string, specFile string, noZip bool, keepURL bool) (*metav1.ObjectMeta, error) {

	pkgSpec := fv1.PackageSpec{
		Environment: fv1.EnvironmentReference{
			Namespace: envNamespace,
			Name:      envName,
		},
	}
	var pkgStatus fv1.BuildStatus = fv1.BuildStatusSucceeded

	if len(deployArchiveFiles) > 0 {
		if len(specFile) > 0 { // we should do this in all cases, i think
			pkgStatus = fv1.BuildStatusNone
		}
		deployment, err := CreateArchive(client, deployArchiveFiles, noZip, keepURL, specDir, specFile)
		if err != nil {
			return nil, err
		}
		pkgSpec.Deployment = *deployment
		if len(pkgName) == 0 {
			pkgName = util.KubifyName(fmt.Sprintf("%v-%v", path.Base(deployArchiveFiles[0]), uniuri.NewLen(4)))
		}
	}
	if len(srcArchiveFiles) > 0 {
		source, err := CreateArchive(client, srcArchiveFiles, false, keepURL, specDir, specFile)
		if err != nil {
			return nil, err
		}
		pkgSpec.Source = *source
		pkgStatus = fv1.BuildStatusPending // set package build status to pending
		if len(pkgName) == 0 {
			pkgName = util.KubifyName(fmt.Sprintf("%v-%v", path.Base(srcArchiveFiles[0]), uniuri.NewLen(4)))
		}
	}

	if len(buildcmd) > 0 {
		pkgSpec.BuildCommand = buildcmd
	}

	if len(pkgName) == 0 {
		pkgName = strings.ToLower(uuid.NewV4().String())
	}
	pkg := &fv1.Package{
		Metadata: metav1.ObjectMeta{
			Name:      pkgName,
			Namespace: pkgNamespace,
		},
		Spec: pkgSpec,
		Status: fv1.PackageStatus{
			BuildStatus:         pkgStatus,
			LastUpdateTimestamp: time.Now().UTC(),
		},
	}

	if len(specFile) > 0 {
		// if a package sith the same spec exists, don't create a new spec file
		fr, err := spec.ReadSpecs(util.GetSpecDir(input))
		if err != nil {
			return nil, errors.Wrap(err, "error reading specs")
		}
		if m := fr.SpecExists(pkg, false, true); m != nil {
			fmt.Printf("Re-using previously created package %v\n", m.Name)
			return m, nil
		}

		err = spec.SpecSave(*pkg, specFile)
		if err != nil {
			return nil, errors.Wrap(err, "error saving package spec")
		}
		return &pkg.Metadata, nil
	} else {
		pkgMetadata, err := client.PackageCreate(pkg)
		if err != nil {
			return nil, errors.Wrap(err, "error creating package")
		}
		fmt.Printf("Package '%v' created\n", pkgMetadata.GetName())
		return pkgMetadata, nil
	}
}
