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

package spec

import (
	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DestroySubCommand struct {
	cmd.CommandActioner
}

// Destroy destroys everything in the spec.
func Destroy(input cli.Input) error {
	return (&DestroySubCommand{}).do(input)
}

func (opts *DestroySubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *DestroySubCommand) run(input cli.Input) error {
	// get specdir and specignore
	specDir := util.GetSpecDir(input)
	specIgnore := util.GetSpecIgnore(input)

	// read everything
	fr, err := ReadSpecs(specDir, specIgnore, false)
	if err != nil {
		return errors.Wrap(err, "error reading specs")
	}

	// set desired state to nothing, but keep the UID so "apply" can find it
	emptyFr := FissionResources{}
	emptyFr.DeploymentConfig = fr.DeploymentConfig

	// "apply" the empty state
	err = deleteResources(opts.Client(), &emptyFr)
	if err != nil {
		return errors.Wrap(err, "error deleting resources")
	}

	return nil
}

// TODO: we don't have namespace here. It uses UUID. Do we need to add namespace here
func deleteResources(fclient client.Interface, fr *FissionResources) error {

	var err error

	_, _, err = applyHTTPTriggers(fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "HTTPTrigger delete failed")
	}

	_, _, err = applyKubernetesWatchTriggers(fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "KubernetesWatchTrigger delete failed")
	}

	_, _, err = applyTimeTriggers(fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "TimeTrigger delete failed")
	}

	_, _, err = applyMessageQueueTriggers(fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "MessageQueueTrigger delete failed")
	}

	_, _, err = applyFunctions(fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "function delete failed")
	}

	_, _, err = applyPackages(fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "package delete failed")
	}

	_, _, err = applyEnvironments(fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "environment delete failed")
	}

	return nil
}
