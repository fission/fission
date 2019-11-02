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

package function

import (
	"github.com/pkg/errors"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

type GetSubCommand struct {
	client *client.Client
}

func Get(flags cli.Input) error {
	opts := GetSubCommand{
		client: cmd.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *GetSubCommand) do(flags cli.Input) error {
	m, err := cmd.GetMetadata("name", "fnNamespace", flags)
	if err != nil {
		return err
	}

	fn, err := opts.client.FunctionGet(m)
	if err != nil {
		return errors.Wrap(err, "error getting function")
	}

	pkg, err := opts.client.PackageGet(&metav1.ObjectMeta{
		Name:      fn.Spec.Package.PackageRef.Name,
		Namespace: fn.Spec.Package.PackageRef.Namespace,
	})
	if err != nil {
		return errors.Wrap(err, "error getting package")
	}

	os.Stdout.Write(pkg.Spec.Deployment.Literal)

	return nil
}
