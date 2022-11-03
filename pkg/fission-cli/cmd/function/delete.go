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
	"fmt"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

	_, namespace, err := util.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return errors.Wrap(err, "error in deleting function ")
	}
	m := &metav1.ObjectMeta{
		Name:      input.String(flagkey.FnName),
		Namespace: namespace,
	}

	err = opts.Client().FissionClientSet.CoreV1().Functions(namespace).Delete(input.Context(), input.String(flagkey.FnName), metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && util.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, fmt.Sprintf("delete function '%s'", m.Name))
	}

	fmt.Printf("function '%s' deleted\n", m.Name)
	return nil
}
