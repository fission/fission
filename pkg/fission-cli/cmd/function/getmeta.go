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

type GetMetaSubCommand struct {
	cmd.CommandActioner
}

func GetMeta(input cli.Input) error {
	return (&GetMetaSubCommand{}).do(input)
}

func (opts *GetMetaSubCommand) do(input cli.Input) error {
	_, namespace, err := util.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return errors.Wrap(err, "error in getting meta function ")
	}

	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "error getting function")
	}

	fmt.Printf("Name: %v\n", fn.ObjectMeta.Name)
	fmt.Printf("Environment: %v\n", fn.Spec.Environment.Name)
	if len(fn.ObjectMeta.Labels) != 0 {
		fmt.Println("Labels:")
		for k, v := range fn.ObjectMeta.Labels {
			fmt.Printf("  %s=%s\n", k, v)
		}
	}
	if len(fn.ObjectMeta.Annotations) != 0 {
		fmt.Println("Annotations:")
		for k, v := range fn.ObjectMeta.Annotations {
			fmt.Printf("  %s=%s\n", k, v)
		}
	}

	return nil
}
