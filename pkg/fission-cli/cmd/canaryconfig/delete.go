// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

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
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceCanary)
	if err != nil {
		return fmt.Errorf("error in deleting canaryConfig: %w", err)
	}

	err = opts.Client().FissionClientSet.CoreV1().CanaryConfigs(namespace).Delete(input.Context(), input.String(flagkey.CanaryName), metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && util.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error deleting canary config: %w", err)
	}

	fmt.Printf("canaryconfig '%v.%v' deleted\n", input.String(flagkey.CanaryName), namespace)
	return nil
}
