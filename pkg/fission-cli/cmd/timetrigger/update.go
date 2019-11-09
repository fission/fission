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

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	client  *client.Client
	trigger *fv1.TimeTrigger
}

func Update(input cli.Input) error {
	c, err := util.GetServer(input)
	if err != nil {
		return err
	}
	opts := UpdateSubCommand{
		client: c,
	}
	return opts.do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateSubCommand) complete(input cli.Input) error {
	tt, err := opts.client.TimeTriggerGet(&metav1.ObjectMeta{
		Name:      input.String(flagkey.TtName),
		Namespace: input.String(flagkey.NamespaceTrigger),
	})
	if err != nil {
		return errors.Wrap(err, "error getting time trigger")
	}

	updated := false
	newCron := input.String("cron")
	if len(newCron) != 0 {
		tt.Spec.Cron = newCron
		updated = true
	}

	// TODO : During update, function has to be in the same ns as the trigger object
	// but since we are not checking this for other triggers too, not sure if we need a check here.

	fnName := input.String("function")
	if len(fnName) > 0 {
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
	_, err := opts.client.TimeTriggerUpdate(opts.trigger)
	if err != nil {
		return errors.Wrap(err, "error updating Time trigger")
	}

	fmt.Printf("trigger '%v' updated\n", opts.trigger.Metadata.Name)

	t, err := getAPITimeInfo(opts.client)
	if err != nil {
		return err
	}

	err = getCronNextNActivationTime(opts.trigger.Spec.Cron, t, 1)
	if err != nil {
		return errors.Wrap(err, "error passing cron spec examination")
	}

	return nil
}
