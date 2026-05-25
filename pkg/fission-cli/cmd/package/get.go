// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"bytes"
	"fmt"
	"io"
	"os"

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
		return fv1.AggregateValidationErrors("Package", err)
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

	switch archive.Type {
	case fv1.ArchiveTypeLiteral:
		reader = bytes.NewReader(archive.Literal)
	case fv1.ArchiveTypeUrl:
		readCloser, err := pkgutil.DownloadStrorageURL(input.Context(), opts.Client(), archive.URL)
		if err != nil {
			return fmt.Errorf("error downloading from storage service url: %s: %w", archive.URL, err)
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
