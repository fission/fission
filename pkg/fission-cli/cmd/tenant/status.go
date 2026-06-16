// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

func Status(input cli.Input) error {
	return (&StatusSubCommand{}).do(input)
}

type StatusSubCommand struct {
	cmd.CommandActioner
}

func (opts *StatusSubCommand) do(input cli.Input) error {
	namespace := input.String(flagkey.Namespace)
	if namespace == "" {
		return fmt.Errorf("--%s is required: the tenant namespace", flagkey.Namespace)
	}

	ft, err := opts.Client().FissionClientSet.CoreV1().FissionTenants().Get(input.Context(), namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting tenant %q: %w", namespace, err)
	}

	fnNS := ft.Spec.FunctionNamespace
	if fnNS == "" {
		fnNS = "(same as namespace)"
	}
	builderNS := ft.Spec.BuilderNamespace
	if builderNS == "" {
		builderNS = "(same as namespace)"
	}

	fmt.Printf("Name:               %s\n", ft.Name)
	fmt.Printf("Namespace:          %s\n", ft.Spec.Namespace)
	fmt.Printf("Function Namespace: %s\n", fnNS)
	fmt.Printf("Builder Namespace:  %s\n", builderNS)
	fmt.Printf("Source:             %s\n", managedBySource(ft.Annotations))
	fmt.Println()
	util.PrintConditions(ft.Status.Conditions)
	return nil
}
