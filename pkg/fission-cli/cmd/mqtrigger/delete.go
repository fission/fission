// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
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

	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in deleting message queue trigger : %w", err)
	}

	opts.metadata = &metav1.ObjectMeta{
		Name:      input.String(flagkey.MqtName),
		Namespace: namespace,
	}
	return nil
}

func (opts *DeleteSubCommand) run(input cli.Input) error {
	deleted, err := util.DeleteOne(input, opts.Client().FissionClientSet.CoreV1().MessageQueueTriggers(opts.metadata.Namespace), opts.metadata.Name, "message queue trigger")
	if err != nil {
		return err
	}
	if deleted {
		fmt.Printf("trigger '%v' deleted\n", opts.metadata.Name)
	}
	return nil
}
