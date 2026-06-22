// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

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

// Wait blocks until the function reaches the --for condition or the timeout.
func Wait(input cli.Input) error {
	return (&WaitSubCommand{}).do(input)
}

func (opts *WaitSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return err
	}
	name := input.String(flagkey.FnName)

	get := func(ctx context.Context) ([]metav1.Condition, error) {
		fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return fn.Status.Conditions, nil
	}
	return util.RunWait(input, "Function", name, get)
}
