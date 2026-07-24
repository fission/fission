// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type WaitSubCommand struct {
	cmd.CommandActioner
}

// Wait blocks until the FunctionAlias reaches the --for condition or the
// timeout — e.g. `fission alias wait --name prod --for condition=Resolved`
// after an `alias update`, so a caller (CI, `fn rollback`) can tell when the
// alias resolver has actually converged on the new target rather than
// racing it.
func Wait(input cli.Input) error {
	return (&WaitSubCommand{}).do(input)
}

func (opts *WaitSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return err
	}
	name := input.String(flagkey.AliasName)

	get := func(ctx context.Context) ([]metav1.Condition, error) {
		obj, err := opts.Client().FissionClientSet.CoreV1().FunctionAliases(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return obj.Status.Conditions, nil
	}
	return util.RunWait(input, "FunctionAlias", name, get)
}
