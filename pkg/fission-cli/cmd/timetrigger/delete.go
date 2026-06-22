// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timetrigger

import (
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) (err error) {

	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in deleting time trigger : %w", err)
	}

	err = opts.Client().FissionClientSet.CoreV1().TimeTriggers(namespace).Delete(input.Context(), input.String(flagkey.TtName), metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && kerrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error deleting trigger: %w", err)
	}

	fmt.Printf("trigger '%v' deleted\n", input.String(flagkey.TtName))
	return nil
}
