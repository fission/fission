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
	"bytes"
	"io"
	"os"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	pkgutil "github.com/fission/fission/pkg/fission-cli/cmd/package/util"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type GetSubCommand struct {
	cmd.CommandActioner
	name        string
	namespace   string
	output      string
	archiveType string
}

func GetSrc(input cli.Input) error {
	opts := &GetSubCommand{}
	opts.archiveType = util.SOURCE_ARCHIVE
	return opts.do(input)
}

func GetDeploy(input cli.Input) error {
	opts := &GetSubCommand{}
	opts.archiveType = util.DEPLOY_ARCHIVE
	return opts.do(input)
}

func (opts *GetSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *GetSubCommand) complete(input cli.Input) (err error) {
	opts.name = input.String(flagkey.PkgName)
	_, opts.namespace, err = opts.GetResourceNamespace(input, flagkey.NamespacePackage)
	if err != nil {
		return fv1.AggregateValidationErrors("Environment", err)
	}
	opts.output = input.String(flagkey.PkgOutput)
	return nil
}

func (opts *GetSubCommand) run(input cli.Input) error {
	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(opts.namespace).Get(input.Context(), opts.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	var reader io.Reader
	archive := pkg.Spec.Source
	if (opts.archiveType == util.DEPLOY_ARCHIVE || archive.Type == "") && (pkg.Spec.Deployment.Type != "") {
		archive = pkg.Spec.Deployment
	}

	if archive.Type == fv1.ArchiveTypeLiteral {
		reader = bytes.NewReader(archive.Literal)
	} else if archive.Type == fv1.ArchiveTypeUrl {

		readCloser, err := pkgutil.DownloadStrorageURL(input.Context(), opts.Client(), archive.URL)
		if err != nil {
			return errors.Wrapf(err, "error downloading from storage service url: %s", archive.URL)
		}

		defer readCloser.Close()
		reader = readCloser
	}

	if len(opts.output) > 0 {
		return pkgutil.WriteArchiveToFile(opts.output, reader)
	} else {
		_, err := io.Copy(os.Stdout, reader)
		return err
	}
}
