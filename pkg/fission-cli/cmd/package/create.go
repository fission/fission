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
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	cmd.CommandActioner
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
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
		if input.Bool(flagkey.SpecSave) && len(input.String(flagkey.PkgName)) == 0 {
			return errors.Errorf("--%v is necessary when creating spec file", flagkey.PkgName)
		} else {
			console.Warn(fmt.Sprintf("--%v will be soon marked as required flag, see 'help' for details", flagkey.HtName))
		}
	}
	pkgNamespace := input.String(flagkey.NamespacePackage)
	envName := input.String(flagkey.PkgEnvironment)
	envNamespace := input.String(flagkey.NamespaceEnvironment)
	srcArchiveFiles := input.StringSlice(flagkey.PkgSrcArchive)
	deployArchiveFiles := input.StringSlice(flagkey.PkgDeployArchive)
	buildcmd := input.String(flagkey.PkgBuildCmd)

	noZip := false
	code := input.String(flagkey.PkgCode)
	if len(code) == 0 {
		deployArchiveFiles = input.StringSlice(flagkey.PkgDeployArchive)
	} else {
		deployArchiveFiles = append(deployArchiveFiles, input.String(flagkey.PkgCode))
		noZip = true
	}

	if len(srcArchiveFiles) == 0 && len(deployArchiveFiles) == 0 {
		return errors.Errorf("need --%v or --%v or --%v argument", flagkey.PkgCode, flagkey.PkgSrcArchive, flagkey.PkgDeployArchive)
	}

	var specDir, specFile string

	if input.Bool(flagkey.SpecSave) {
		specDir = util.GetSpecDir(input)
		fr, err := spec.ReadSpecs(specDir)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error reading spec in '%v'", specDir))
		}
		exists, err := fr.ExistsInSpecs(fv1.Environment{
			Metadata: metav1.ObjectMeta{
				Name:      envName,
				Namespace: envNamespace,
			},
		})
		if err != nil {
			return err
		}
		if !exists {
			console.Warn(fmt.Sprintf("Package '%v' references unknown Environment '%v', please create it before applying spec",
				pkgName, envName))
		}

		specDir = util.GetSpecDir(input)
		specFile = fmt.Sprintf("package-%v.yaml", pkgName)
	}

	_, err := CreatePackage(input, opts.Client(), pkgName, pkgNamespace, envName, envNamespace,
		srcArchiveFiles, deployArchiveFiles, buildcmd, specDir, specFile, noZip)

	return err
}

// TODO: get all necessary value from CLI input directly
func CreatePackage(input cli.Input, client client.Interface, pkgName string, pkgNamespace string, envName string, envNamespace string,
	srcArchiveFiles []string, deployArchiveFiles []string, buildcmd string, specDir string, specFile string, noZip bool) (*metav1.ObjectMeta, error) {

	insecure := input.Bool(flagkey.PkgInsecure)
	deployChecksum := input.String(flagkey.PkgDeployChecksum)
	srcChecksum := input.String(flagkey.PkgSrcChecksum)

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
		deployment, err := CreateArchive(client, deployArchiveFiles, noZip, insecure, deployChecksum, specDir, specFile)
		if err != nil {
			return nil, errors.Wrap(err, "error creating source archive")
		}
		pkgSpec.Deployment = *deployment
		if len(pkgName) == 0 {
			pkgName = util.KubifyName(fmt.Sprintf("%v-%v", path.Base(deployArchiveFiles[0]), uniuri.NewLen(4)))
		}
	}
	if len(srcArchiveFiles) > 0 {
		source, err := CreateArchive(client, srcArchiveFiles, false, insecure, srcChecksum, specDir, specFile)
		if err != nil {
			return nil, errors.Wrap(err, "error creating deploy archive")
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
		// if a package with the same spec exists, don't create a new spec file
		fr, err := spec.ReadSpecs(util.GetSpecDir(input))
		if err != nil {
			return nil, errors.Wrap(err, "error reading specs")
		}

		obj := fr.SpecExists(pkg, true, true)
		if obj != nil {
			pkg := obj.(*fv1.Package)
			fmt.Printf("Re-using previously created package %v\n", pkg.Metadata.Name)
			return &pkg.Metadata, nil
		}

		err = spec.SpecSave(*pkg, specFile)
		if err != nil {
			return nil, errors.Wrap(err, "error saving package spec")
		}
		return &pkg.Metadata, nil
	} else {
		pkgMetadata, err := client.V1().Package().Create(pkg)
		if err != nil {
			return nil, errors.Wrap(err, "error creating package")
		}
		fmt.Printf("Package '%v' created\n", pkgMetadata.GetName())
		return pkgMetadata, nil
	}
}
