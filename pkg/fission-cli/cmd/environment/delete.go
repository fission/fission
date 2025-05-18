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
	"errors"
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) (err error) {

	_, currentContextNS, err := opts.GetResourceNamespace(input, flagkey.NamespaceEnvironment)
	if err != nil {
		return fmt.Errorf("error creating environment: %w", err)
	}
	console.Verbose(2, "Searching for resource in  %s Namespace", currentContextNS)
	envName := input.String(flagkey.EnvName)

	if !input.Bool(flagkey.EnvForce) {
		fns, err := opts.Client().FissionClientSet.CoreV1().Functions(metav1.NamespaceAll).List(input.Context(), metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("error getting functions wrt environment: %w", err)
		}

		for _, fn := range fns.Items {
			if fn.Spec.Environment.Name == envName &&
				fn.Spec.Environment.Namespace == currentContextNS {
				return errors.New("environment is used by at least one function")
			}
		}
	}

	err = opts.Client().FissionClientSet.CoreV1().Environments(currentContextNS).Delete(input.Context(), envName, metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && kerrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error deleting environment: %w", err)
	}

	fmt.Printf("environment '%s' deleted\n", envName)

	return nil
}
