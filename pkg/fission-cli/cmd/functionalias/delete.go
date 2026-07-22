// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

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
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).run(input)
}

func (opts *DeleteSubCommand) run(input cli.Input) (err error) {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in deleting function alias: %w", err)
	}

	name := input.String(flagkey.AliasName)
	err = opts.Client().FissionClientSet.CoreV1().FunctionAliases(namespace).Delete(input.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && util.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error deleting function alias: %w", err)
	}

	fmt.Printf("function alias '%v.%v' deleted\n", name, namespace)
	return nil
}
