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

package httptrigger

import (
	"fmt"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
	triggerName  string
	functionName string
	namespace    string
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *DeleteSubCommand) complete(input cli.Input) (err error) {
	opts.triggerName = input.String(flagkey.HtName)
	opts.functionName = input.String(flagkey.HtFnName)
	if len(opts.triggerName) == 0 && len(opts.functionName) == 0 {
		return errors.Errorf("need --%v or --%v", flagkey.HtName, flagkey.HtFnName)
	} else if len(opts.triggerName) > 0 && len(opts.functionName) > 0 {
		return errors.Errorf("need either of --%v or --%v and not both arguments", flagkey.HtName, flagkey.HtFnName)
	}

	_, opts.namespace, err = util.GetResourceNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return errors.Wrap(err, "error in deleting function ")
	}
	return nil
}

func (opts *DeleteSubCommand) run(input cli.Input) error {
	triggers, err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(opts.namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return errors.Wrap(err, "error getting HTTP trigger list")
	}

	var triggersToDelete []string

	if len(opts.functionName) > 0 {
		for _, trigger := range triggers.Items {
			// TODO: delete canary http triggers as well.
			if trigger.Spec.FunctionReference.Name == opts.functionName {
				triggersToDelete = append(triggersToDelete, trigger.ObjectMeta.Name)
			}
		}
	} else {
		triggersToDelete = []string{opts.triggerName}
	}

	errs := utils.MultiErrorWithFormat()

	for _, name := range triggersToDelete {
		err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(opts.namespace).Delete(input.Context(), name, metav1.DeleteOptions{})
		if err != nil {
			errs = multierror.Append(errs, err)
		} else {
			fmt.Printf("trigger '%v' deleted\n", name)
		}
	}

	if errs.ErrorOrNil() != nil {
		if input.Bool(flagkey.IgnoreNotFound) && util.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(errs.ErrorOrNil(), "error deleting trigger(s)")
	}

	return nil
}
