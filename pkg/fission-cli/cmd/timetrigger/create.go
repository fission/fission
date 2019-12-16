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
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"time"

	"github.com/pkg/errors"
	"github.com/robfig/cron"
	uuid "github.com/satori/go.uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	trigger *fv1.TimeTrigger
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
	name := input.String(flagkey.TtName)
	if len(name) == 0 {
		name = uuid.NewV4().String()
	}

	fnName := input.String(flagkey.TtFnName)
	if len(fnName) == 0 {
		return errors.New("Need a function name to create a trigger, use --function")
	}

	fnNamespace := input.String(flagkey.NamespaceFunction)

	cronSpec := input.String(flagkey.TtCron)
	if len(cronSpec) == 0 {
		return errors.New("Need a cron spec like '0 30 * * * *', '@every 1h30m', or '@hourly'; use --cron")
	}

	if input.Bool(flagkey.SpecSave) {
		specDir := util.GetSpecDir(input)
		fr, err := spec.ReadSpecs(specDir)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error reading spec in '%v'", specDir))
		}

		exists, err := fr.ExistsInSpecs(fv1.Function{
			Metadata: metav1.ObjectMeta{
				Name:      fnName,
				Namespace: fnNamespace,
			},
		})
		if err != nil {
			return err
		}
		if !exists {
			console.Warn(fmt.Sprintf("TimeTrigger '%v' references unknown Function '%v', please create it before applying spec",
				name, fnName))
		}
	}

	opts.trigger = &fv1.TimeTrigger{
		Metadata: metav1.ObjectMeta{
			Name:      name,
			Namespace: fnNamespace,
		},
		Spec: fv1.TimeTriggerSpec{
			Cron: cronSpec,
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: fnName,
			},
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	// if we're writing a spec, don't call the API
	if input.Bool(flagkey.SpecSave) {
		specFile := fmt.Sprintf("timetrigger-%v.yaml", opts.trigger.Metadata.Name)
		err := spec.SpecSave(*opts.trigger, specFile)
		if err != nil {
			return errors.Wrap(err, "error creating time trigger spec")
		}
		return nil
	}

	_, err := opts.Client().V1().TimeTrigger().Create(opts.trigger)
	if err != nil {
		return errors.Wrap(err, "error creating Time trigger")
	}

	fmt.Printf("trigger '%v' created\n", opts.trigger.Metadata.Name)

	t, err := getAPITimeInfo(opts.Client())
	if err != nil {
		return err
	}

	err = getCronNextNActivationTime(opts.trigger.Spec.Cron, t, 1)
	if err != nil {
		return errors.Wrap(err, "error passing cron spec examination")
	}

	return nil
}

func getAPITimeInfo(client client.Interface) (time.Time, error) {
	serverInfo, err := client.V1().Misc().ServerInfo()
	if err != nil {
		return time.Time{}, errors.Errorf("Error syncing server time information: %v", err)
	}
	return serverInfo.ServerTime.CurrentTime, nil
}

func getCronNextNActivationTime(cronSpec string, serverTime time.Time, round int) error {
	sched, err := cron.Parse(cronSpec)
	if err != nil {
		return err
	}

	fmt.Printf("Current Server Time: \t%v\n", serverTime.Format(time.RFC3339))

	for i := 0; i < round; i++ {
		serverTime = sched.Next(serverTime)
		fmt.Printf("Next %v invocation: \t%v\n", i+1, serverTime.Format(time.RFC3339))
	}

	return nil
}
