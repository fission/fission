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

package environment

import (
	"fmt"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {
	m := &metav1.ObjectMeta{
		Name:      input.String(flagkey.EnvName),
		Namespace: input.String(flagkey.NamespaceEnvironment),
	}

	err := opts.Client().V1().Environment().Delete(m)
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && util.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "error deleting environment")
	}

	fmt.Printf("environment '%v' deleted\n", m.Name)

	fns, err := opts.Client().V1().Function().List(metav1.NamespaceAll)
	if err != nil {
		return errors.Wrap(err, "Error updating function environment")
	}

	for _, fn := range fns {
		if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeNewdeploy &&
			fn.Spec.Environment.Name == m.Name {
			funcm := &metav1.ObjectMeta{
				Name:      fn.Name,
				Namespace: fn.Namespace,
			}
			err := opts.Client().V1().Function().Delete(funcm)
			if err != nil {
				return errors.Wrap(err, "error deleting functions associated with env")
			}
		}
	}
	return nil
}
