// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type GetSubCommand struct {
	cmd.CommandActioner
}

func Get(input cli.Input) error {
	return (&GetSubCommand{}).do(input)
}

func (opts *GetSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return fmt.Errorf("error in get function : %w", err)
	}
	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function: %w", err)
	}

	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(fn.Spec.Package.PackageRef.Namespace).Get(input.Context(), fn.Spec.Package.PackageRef.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting package: %w", err)
	}

	os.Stdout.Write(pkg.Spec.Deployment.Literal)

	return nil
}
