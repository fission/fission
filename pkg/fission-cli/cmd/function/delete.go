// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

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
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {

	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return fmt.Errorf("error in deleting function : %w", err)
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
		return fmt.Errorf("delete function '%s': %w", m.Name, err)
	}

	fmt.Printf("function '%s' deleted\n", m.Name)
	return nil
}
