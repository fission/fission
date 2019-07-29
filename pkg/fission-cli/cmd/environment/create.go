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
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/log"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	client *client.Client
	env    *fv1.Environment
}

func Create(flags cli.Input) error {
	opts := CreateSubCommand{
		client: cmd.GetServer(flags),
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
	env, err := createEnvironmentFromCmd(flags)
	if err != nil {
		return err
	}
	opts.env = env
	return nil
}

func (opts *CreateSubCommand) run(flags cli.Input) error {
	m, err := cmd.GetMetadata(flags)
	if err != nil {
		return err
	}
	envList, err := opts.client.EnvironmentList(m.Namespace)
	if err != nil {
		return err
	} else if len(envList) > 0 {
		log.Verbose(2, "%d environment(s) are present in the %s namespace.  "+
			"These environments are not isolated from each other; use separate namespaces if you need isolation.",
			len(envList), m.Namespace)
	}

	// if we're writing a spec, don't call the API
	// save to spec file
	if flags.Bool(cmd.SPEC_SPEC) {
		specFile := fmt.Sprintf("env-%v.yaml", m.Name)
		err = spec.SpecSave(opts.env, specFile)
		util.CheckErr(err, "create environment spec")
		return nil
	}

	_, err = opts.client.EnvironmentCreate(opts.env)
	util.CheckErr(err, "create environment")

	fmt.Printf("environment '%v' created\n", m.Name)
	return nil
}

// createEnvironmentFromCmd creates environment initialized with CLI input.
func createEnvironmentFromCmd(flags cli.Input) (*fv1.Environment, error) {
	e := &multierror.Error{}

	envNamespace := flags.String(cmd.ENVIRONMENT_NAMESPACE)
	envBuildCmd := flags.String(cmd.ENVIRONMENT_BUILDCOMMAND)
	envExternalNetwork := flags.Bool(cmd.ENVIRONMENT_EXTERNAL_NETWORK)
	keepArchive := flags.Bool(cmd.ENVIRONMENT_KEEPARCHIVE)

	envName := flags.String(cmd.RESOURCE_NAME)
	if len(envName) == 0 {
		e = multierror.Append(e, errors.New("Need a name, use --name."))
	}

	envImg := flags.String(cmd.ENVIRONMENT_IMAGE)
	if len(envImg) == 0 {
		e = multierror.Append(e, errors.New("Need an image, use --image."))
	}

	envGracePeriod := flags.Int64(cmd.ENVIRONMENT_GRACE_PERIOD)
	if envGracePeriod <= 0 {
		envGracePeriod = 360
	}

	envVersion := flags.Int(cmd.ENVIRONMENT_VERSION)
	// Environment API interface version is not specified and
	// builder image is empty, set default interface version
	if envVersion == 0 {
		envVersion = 1
	}

	envBuilderImg := flags.String(cmd.ENVIRONMENT_BUILDER)
	if len(envBuilderImg) > 0 {
		if !flags.IsSet(cmd.ENVIRONMENT_VERSION) {
			// TODO: remove set env version to 2 silently, we need to warn user to set it explicitly.
			envVersion = 2
		}
		if len(envBuildCmd) == 0 {
			envBuildCmd = "build"
		}
	}

	poolsize := 3
	if flags.IsSet(cmd.ENVIRONMENT_POOLSIZE) {
		poolsize = flags.Int(cmd.ENVIRONMENT_POOLSIZE)
		// TODO: remove silently version 3 assignment, we need to warn user to set it explicitly.
		envVersion = 3
	}

	resourceReq, err := cmd.GetResourceReqs(flags, nil)
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
		},
	}

	err = env.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Environment", err)
	}

	return env, nil
}
