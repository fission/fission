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

package environment

import (
	"fmt"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	env *fv1.Environment
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

// complete creates a environment objects and populates it with default value and CLI inputs.
func (opts *CreateSubCommand) complete(input cli.Input) error {
	env, err := createEnvironmentFromCmd(input)
	if err != nil {
		return err
	}
	opts.env = env
	return nil
}

// run write the resource to a spec file or create a fission CRD with remote fission server.
// It also prints warning/error if necessary.
func (opts *CreateSubCommand) run(input cli.Input) error {
	m := opts.env.Metadata

	envList, err := opts.Client().V1().Environment().List(m.Namespace)
	if err != nil {
		return err
	} else if len(envList) > 0 {
		console.Verbose(2, "%d environment(s) are present in the %s namespace.  "+
			"These environments are not isolated from each other; use separate namespaces if you need isolation.",
			len(envList), m.Namespace)
	}

	// if we're writing a spec, don't call the API
	// save to spec file
	if input.Bool(flagkey.SpecSave) {
		specFile := fmt.Sprintf("env-%v.yaml", m.Name)
		err = spec.SpecSave(*opts.env, specFile)
		if err != nil {
			return errors.Wrap(err, "error creating environment spec")
		}
		return nil
	}

	_, err = opts.Client().V1().Environment().Create(opts.env)
	if err != nil {
		return errors.Wrap(err, "error creating environment")
	}

	fmt.Printf("environment '%v' created\n", m.Name)
	return nil
}

// createEnvironmentFromCmd creates environment initialized with CLI input.
func createEnvironmentFromCmd(input cli.Input) (*fv1.Environment, error) {
	e := utils.MultiErrorWithFormat()

	envName := input.String(flagkey.EnvName)
	envImg := input.String(flagkey.EnvImage)
	envNamespace := input.String(flagkey.NamespaceEnvironment)
	envBuildCmd := input.String(flagkey.EnvBuildcommand)
	envExternalNetwork := input.Bool(flagkey.EnvExternalNetwork)
	keepArchive := input.Bool(flagkey.EnvKeeparchive)
	envGracePeriod := input.Int64(flagkey.EnvGracePeriod)
	pullSecret := input.String(flagkey.EnvImagePullSecret)

	envVersion := input.Int(flagkey.EnvVersion)
	// Environment API interface version is not specified and
	// builder image is empty, set default interface version
	if envVersion == 0 {
		envVersion = 1
	}

	poolsize := input.Int(flagkey.EnvPoolsize)
	if input.IsSet(flagkey.EnvPoolsize) {
		// TODO: remove silently version 3 assignment, we need to warn user to set it explicitly.
		envVersion = 3
	}

	envBuilderImg := input.String(flagkey.EnvBuilderImage)
	if len(envBuilderImg) > 0 {
		if !input.IsSet(flagkey.EnvVersion) {
			// TODO: remove set env version to 2 silently, we need to warn user to set it explicitly.
			envVersion = 2
		}
		if len(envBuildCmd) == 0 {
			envBuildCmd = "build"
		}
	}

	resourceReq, err := util.GetResourceReqs(input, nil)
	if err != nil {
		e = multierror.Append(e, err)
	}

	if e.ErrorOrNil() != nil {
		return nil, e.ErrorOrNil()
	}

	env := &fv1.Environment{
		TypeMeta: metav1.TypeMeta{
			Kind:       fv1.CRD_NAME_ENVIRONMENT,
			APIVersion: fv1.CRD_VERSION,
		},
		Metadata: metav1.ObjectMeta{
			Name:      envName,
			Namespace: envNamespace,
		},
		Spec: fv1.EnvironmentSpec{
			Version: envVersion,
			Runtime: fv1.Runtime{
				Image: envImg,
			},
			Builder: fv1.Builder{
				Image:   envBuilderImg,
				Command: envBuildCmd,
			},
			Poolsize:                     poolsize,
			Resources:                    *resourceReq,
			AllowAccessToExternalNetwork: envExternalNetwork,
			TerminationGracePeriod:       envGracePeriod,
			KeepArchive:                  keepArchive,
			ImagePullSecret:              pullSecret,
		},
	}

	err = env.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Environment", err)
	}

	return env, nil
}
