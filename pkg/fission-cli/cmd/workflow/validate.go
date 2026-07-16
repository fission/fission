// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type ValidateSubCommand struct {
	cmd.CommandActioner
}

// Validate lints a workflow manifest: the full offline rule set (graph,
// expressions — the same checks admission enforces), plus referenced-function
// existence against the cluster unless --offline. Function existence is a
// warning, never an error: GitOps applies resources in arbitrary order.
func Validate(input cli.Input) error {
	return (&ValidateSubCommand{}).do(input)
}

func (opts *ValidateSubCommand) do(input cli.Input) error {
	wf, err := parseManifest(input)
	if err != nil {
		return err
	}

	if err := wf.Spec.Validate(); err != nil {
		return fv1.AggregateValidationErrors("Workflow", err)
	}

	name := wf.Name
	if name == "" {
		name = input.String(flagkey.WfFile)
	}

	if !input.Bool(flagkey.WfOffline) {
		_, namespace, err := opts.GetResourceNamespace(input)
		if err != nil {
			return fmt.Errorf("error resolving namespace: %w", err)
		}
		for state, st := range wf.Spec.States {
			if st.Function == nil || st.Function.Name == "" {
				continue
			}
			_, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), st.Function.Name, metav1.GetOptions{})
			switch {
			case kerrors.IsNotFound(err):
				console.Warn(fmt.Sprintf("state %q references function %q which does not exist in namespace %q (create it before running the workflow)",
					state, st.Function.Name, namespace))
			case err != nil:
				return fmt.Errorf("checking function %q: %w", st.Function.Name, err)
			}
		}
	}

	fmt.Printf("workflow %s: valid\n", name)
	return nil
}
