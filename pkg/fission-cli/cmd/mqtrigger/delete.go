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

package mqtrigger

import (
	"fmt"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
	metadata *metav1.ObjectMeta
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *DeleteSubCommand) complete(input cli.Input) (err error) {

	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return errors.Wrap(err, "error in deleting function ")
	}

	opts.metadata = &metav1.ObjectMeta{
		Name:      input.String(flagkey.MqtName),
		Namespace: namespace,
	}
	return nil
}

func (opts *DeleteSubCommand) run(input cli.Input) error {
	err := opts.Client().FissionClientSet.CoreV1().MessageQueueTriggers(opts.metadata.Namespace).Delete(input.Context(), opts.metadata.Name, metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && kerrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "error deleting message queue trigger")
	}

	fmt.Printf("trigger '%v' deleted\n", opts.metadata.Name)
	return nil
}
