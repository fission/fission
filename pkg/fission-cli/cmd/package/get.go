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
	"errors"
	"io"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	cmdutils "github.com/fission/fission/pkg/fission-cli/cmd"
	pkgutil "github.com/fission/fission/pkg/fission-cli/cmd/package/util"
)

const (
	deployArchive = iota
	sourceArchive
)

type GetSubCommand struct {
	client      *client.Client
	name        string
	namespace   string
	output      string
	archiveType int
}

func GetSrc(flags cli.Input) error {
	opts := GetSubCommand{
		client:      cmdutils.GetServer(flags),
		archiveType: sourceArchive,
	}
	return opts.do(flags)
}

func GetDeploy(flags cli.Input) error {
	opts := GetSubCommand{
		client:      cmdutils.GetServer(flags),
		archiveType: deployArchive,
	}
	return opts.do(flags)
}

func (opts *GetSubCommand) complete(flags cli.Input) error {
	opts.name = flags.String("name")
	if len(opts.name) == 0 {
		return errors.New("need name of package, use --name")
	}
	opts.namespace = flags.String("pkgNamespace")
	opts.output = flags.String("output")
	return nil
}

func (opts *GetSubCommand) do(flags cli.Input) error {
	pkg, err := opts.client.PackageGet(&metav1.ObjectMeta{
		Namespace: opts.namespace,
		Name:      opts.name,
	})
	if err != nil {
		return err
	}

	var reader io.Reader
	archive := pkg.Spec.Source
	if opts.archiveType == deployArchive {
		archive = pkg.Spec.Deployment
	}

	if pkg.Spec.Deployment.Type == fv1.ArchiveTypeLiteral {
		reader = bytes.NewReader(archive.Literal)
	} else if pkg.Spec.Deployment.Type == fv1.ArchiveTypeUrl {
		readCloser := pkgutil.DownloadStoragesvcURL(opts.client, archive.URL)
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
