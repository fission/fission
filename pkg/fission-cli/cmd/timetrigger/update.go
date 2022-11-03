/*
Copyright 2019 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package timetrigger

import (
	"fmt"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	trigger *fv1.TimeTrigger
}

func Update(input cli.Input) error {
	return (&UpdateSubCommand{}).do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateSubCommand) complete(input cli.Input) error {
	_, namespace, err := util.GetResourceNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return errors.Wrap(err, "error in deleting function ")
	}

	tt, err := opts.Client().FissionClientSet.CoreV1().TimeTriggers(namespace).Get(input.Context(), input.String(flagkey.TtName), metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "error getting time trigger")
	}

	updated := false
	newCron := input.String("cron")
	if len(newCron) != 0 {
		tt.Spec.Cron = newCron
		updated = true
	}

	fnName := input.String("function")
	if len(fnName) > 0 {
		functionList := []string{fnName}
		err := util.CheckFunctionExistence(input.Context(), opts.Client(), functionList, namespace)
		if err != nil {
			console.Warn(err.Error())
		}
		tt.Spec.FunctionReference.Name = fnName
		updated = true
	}

	if !updated {
		return errors.New("nothing to update. Use --cron or --function")
	}

	opts.trigger = tt

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	_, err := opts.Client().FissionClientSet.CoreV1().TimeTriggers(opts.trigger.ObjectMeta.Namespace).Update(input.Context(), opts.trigger, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrap(err, "error updating Time trigger")
	}

	fmt.Printf("trigger '%v' updated\n", opts.trigger.ObjectMeta.Name)

	t := util.GetServerInfo().ServerTime.CurrentTime.UTC()
	if err != nil {
		return err
	}

	err = getCronNextNActivationTime(opts.trigger.Spec.Cron, t, 1)
	if err != nil {
		return errors.Wrap(err, "error passing cron spec examination")
	}

	return nil
}
