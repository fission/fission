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

type GetMetaSubCommand struct {
	cmd.CommandActioner
}

func GetMeta(input cli.Input) error {
	return (&GetMetaSubCommand{}).do(input)
}

func (opts *GetMetaSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in getting meta function : %w", err)
	}

	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}
	if handled, err := util.PrintStructured(format, fn); err != nil || handled {
		return err
	}

	fmt.Printf("Name: %v\n", fn.Name)
	fmt.Printf("Environment: %v\n", fn.Spec.Environment.Name)
	if len(fn.Labels) != 0 {
		fmt.Println("Labels:")
		for k, v := range fn.Labels {
			fmt.Printf("  %s=%s\n", k, v)
		}
	}
	if len(fn.Annotations) != 0 {
		fmt.Println("Annotations:")
		for k, v := range fn.Annotations {
			fmt.Printf("  %s=%s\n", k, v)
		}
	}
	util.PrintConditions(fn.Status.Conditions)

	return nil
}
