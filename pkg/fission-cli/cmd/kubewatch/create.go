// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatch

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/utils/uuid"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	watcher *fv1.KubernetesWatchTrigger
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) error {
	watchName := input.String(flagkey.KwName)
	if len(watchName) == 0 {
		console.Warn(fmt.Sprintf("--%v will be soon marked as required flag, see 'help' for details", flagkey.MqtName))
		watchName = uuid.NewString()
	}
	fnName := input.String(flagkey.KwFnName)

	_, namespace, err := opts.GetResourceNamespace(input, flagkey.KwNamespace)
	if err != nil {
		return fmt.Errorf("error in listing function : %w", err)
	}

	objType := input.String(flagkey.KwObjType)

	if input.Bool(flagkey.SpecSave) {
		if err := spec.CheckFunctionReferencesInSpecs(input, "KubernetesWatchTrigger", watchName, []string{fnName}, namespace); err != nil {
			return err
		}
	}

	opts.watcher = &fv1.KubernetesWatchTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      watchName,
			Namespace: namespace,
		},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Namespace: namespace,
			Type:      objType,
			//LabelSelector: labels,
			FunctionReference: fv1.FunctionReference{
				Name: fnName,
				Type: fv1.FunctionReferenceTypeFunctionName,
			},
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	// if we're writing a spec, don't call the API; save/print and return.
	if handled, err := spec.SaveOrDry(input, *opts.watcher, fmt.Sprintf("kubewatch-%v.yaml", opts.watcher.Name)); handled {
		return err
	}

	_, err := opts.Client().FissionClientSet.CoreV1().KubernetesWatchTriggers(opts.watcher.ObjectMeta.Namespace).Create(input.Context(), opts.watcher, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating kubewatch: %w", err)
	}

	fmt.Printf("trigger '%v' created\n", opts.watcher.Name)
	return nil
}
