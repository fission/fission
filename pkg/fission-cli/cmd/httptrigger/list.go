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
	"github.com/pkg/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {

	_, namespace, err := util.GetResourceNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return errors.Wrap(err, "error in deleting function ")
	}

	var hts *fv1.HTTPTriggerList
	if input.Bool(flagkey.AllNamespaces) {
		hts, err = opts.Client().FissionClientSet.CoreV1().HTTPTriggers(v1.NamespaceAll).List(input.Context(), v1.ListOptions{})
	} else {
		hts, err = opts.Client().FissionClientSet.CoreV1().HTTPTriggers(namespace).List(input.Context(), v1.ListOptions{})
	}

	if err != nil {
		return errors.Wrap(err, "error listing HTTP triggers")
	}

	filterFunctionName := input.String(flagkey.HtFnName)

	var triggers []fv1.HTTPTrigger
	for _, ht := range hts.Items {
		// TODO: list canary http triggers as well.
		if len(filterFunctionName) == 0 ||
			(len(filterFunctionName) > 0 && filterFunctionName == ht.Spec.FunctionReference.Name) {

			triggers = append(triggers, ht)
		}
	}

	printHtSummary(triggers)
	return nil
}
