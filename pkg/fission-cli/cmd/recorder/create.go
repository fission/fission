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

package recorder

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/satori/go.uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	client   *client.Client
	recorder *fv1.Recorder
}

func Create(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := CreateSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *CreateSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *CreateSubCommand) complete(flags cli.Input) error {
	recName := flags.String("name")
	if len(recName) == 0 {
		recName = uuid.NewV4().String()
	}
	fnName := flags.String("function")
	triggersOriginal := flags.StringSlice("trigger")

	// Function XOR triggers can be given
	if len(fnName) == 0 && len(triggersOriginal) == 0 {
		return errors.New("Need to specify at least one function or one trigger, use --function, --trigger")
	}
	if len(fnName) != 0 && len(triggersOriginal) != 0 {
		return errors.New("Can specify either one function or one or more triggers, but not both")
	}

	// TODO: Validate here or elsewhere that all triggers belong to the same namespace

	var triggers []string
	if len(triggersOriginal) != 0 {
		ts := strings.Split(triggersOriginal[0], ",")
		for _, name := range ts {
			if len(name) > 0 {
				triggers = append(triggers, name)
			}
		}
	}
	// TODO: Define appropriate set of policies and defaults
	//retPolicy := flags.String("retention")
	//evictPolicy := flags.String("eviction")

	opts.recorder = &fv1.Recorder{
		Metadata: metav1.ObjectMeta{
			Name:      recName,
			Namespace: "default",
		},
		Spec: fv1.RecorderSpec{
			Name:            recName,
			Function:        fnName,
			Triggers:        triggers,
			RetentionPolicy: "Permanent", // TODO: Implement customizable policies for expiration of records
			EvictionPolicy:  "None",
			Enabled:         true,
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(flags cli.Input) error {
	// If we're writing a spec, don't call the API
	if flags.Bool("spec") {
		specFile := fmt.Sprintf("recorder-%v.yaml", opts.recorder.Metadata.Name)
		err := spec.SpecSave(*opts.recorder, specFile)
		if err != nil {
			return errors.Wrap(err, "error creating recorder spec")
		}
		return nil
	}
	_, err := opts.client.RecorderCreate(opts.recorder)
	if err != nil {
		return errors.Wrap(err, "error creating recorder")
	}

	fmt.Printf("recorder '%s' created\n", opts.recorder.Metadata.Name)
	return nil
}
