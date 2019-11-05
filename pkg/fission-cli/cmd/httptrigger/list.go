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

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	client             *client.Client
	triggerNamespace   string
	filterFunctionName string
}

func List(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := ListSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *ListSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *ListSubCommand) complete(flags cli.Input) error {
	opts.triggerNamespace = flags.String("triggerNamespace")
	opts.filterFunctionName = flags.String("function")
	return nil
}

func (opts *ListSubCommand) run(flags cli.Input) error {
	hts, err := opts.client.HTTPTriggerList(opts.triggerNamespace)
	if err != nil {
		return errors.Wrap(err, "error listing HTTP triggers")
	}

	var triggers []fv1.HTTPTrigger
	for _, ht := range hts {
		// TODO: list canary http triggers as well.
		if len(opts.filterFunctionName) == 0 ||
			(len(opts.filterFunctionName) > 0 && opts.filterFunctionName == ht.Spec.FunctionReference.Name) {

			triggers = append(triggers, ht)
		}
	}

	printHtSummary(triggers)
	return nil
}
