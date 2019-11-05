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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	client   *client.Client
	recorder *fv1.Recorder
}

func Update(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := UpdateSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *UpdateSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *UpdateSubCommand) complete(flags cli.Input) error {
	recName := flags.String("name")
	enable := flags.Bool("enable")
	disable := flags.Bool("disable")
	//retPolicy := flags.String("retention")
	//evictPolicy := flags.String("eviction")
	triggers := flags.StringSlice("trigger")
	function := flags.String("function")

	if enable && disable {
		return errors.New("Cannot enable and disable a recorder simultaneously.")
	}

	// Prevent enable or disable while trying to update other fields. These flags must be standalone.
	if enable || disable {
		if len(triggers) > 0 || len(function) > 0 {
			return errors.New("Enabling or disabling a recorder with other (non-name) flags set is not supported.")
		}
	} else if len(triggers) == 0 && len(function) == 0 {
		return errors.New("Need to specify either a function or trigger(s) for this recorder")
	}

	if len(recName) == 0 {
		return errors.New("Need name of recorder, use --name")
	}

	recorder, err := opts.client.RecorderGet(&metav1.ObjectMeta{
		Name:      recName,
		Namespace: "default",
	})
	if err != nil {
		return errors.Wrap(err, "error getting recorder")
	}

	updated := false

	// TODO: Additional validation on type of supported retention policy, eviction policy

	//if len(retPolicy) > 0 {
	//	recorder.Spec.RetentionPolicy = retPolicy
	//	updated = true
	//}
	//if len(evictPolicy) > 0 {
	//	recorder.Spec.EvictionPolicy = evictPolicy
	//	updated = true
	//}
	if enable {
		recorder.Spec.Enabled = true
		updated = true
	}

	if disable {
		recorder.Spec.Enabled = false
		updated = true
	}

	if len(triggers) > 0 {
		var newTriggers []string
		triggs := strings.Split(triggers[0], ",")
		for _, name := range triggs {
			if len(name) > 0 {
				newTriggers = append(newTriggers, name)
			}
		}
		recorder.Spec.Triggers = newTriggers
		updated = true
	}

	if len(function) > 0 {
		recorder.Spec.Function = function
		updated = true
	}

	if !updated {
		return errors.New("Nothing to update. Use --function, --triggers, --enable or --disable")
	}

	opts.recorder = recorder
	return nil
}

func (opts *UpdateSubCommand) run(flags cli.Input) error {
	_, err := opts.client.RecorderUpdate(opts.recorder)
	if err != nil {
		return errors.Wrap(err, "error updating recorder")
	}

	fmt.Printf("recorder '%v' updated\n", opts.recorder.Metadata.Name)
	return nil
}
