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

package canaryconfig

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
	return (&DeleteSubCommand{}).run(input)
}

func (opts *DeleteSubCommand) run(input cli.Input) (err error) {
	_, namespace, err := util.GetResourceNamespace(input, flagkey.NamespaceCanary)
	if err != nil {
		return errors.Wrap(err, "error in deleting canaryConfig ")
	}

	err = opts.Client().FissionClientSet.CoreV1().CanaryConfigs(namespace).Delete(input.Context(), input.String(flagkey.CanaryName), metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && util.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "error deleting canary config")
	}

	fmt.Printf("canaryconfig '%v.%v' deleted\n", input.String(flagkey.CanaryName), namespace)
	return nil
}
