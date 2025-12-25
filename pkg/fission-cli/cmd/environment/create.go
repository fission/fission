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
	"errors"
	"fmt"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
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
func (opts *CreateSubCommand) run(input cli.Input) (err error) {

	userDefinedNS, currentNS, err := opts.GetResourceNamespace(input, flagkey.NamespaceEnvironment)
	if err != nil {
		return fv1.AggregateValidationErrors("Environment", err)
	}
	// we use user provided NS in spec. While creating actual record we use the current context's NS.
	opts.env.ObjectMeta.Namespace = userDefinedNS

	envList, err := opts.Client().FissionClientSet.CoreV1().Environments(opts.env.ObjectMeta.Namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return err
	} else if len(envList.Items) > 0 {
		console.Verbose(2, "%d environment(s) are present in the %s namespace.  "+
			"These environments are not isolated from each other; use separate namespaces if you need isolation.",
			len(envList.Items), opts.env.ObjectMeta.Namespace)
	}

	// if we're writing a spec, don't call the API
	// save to spec file or display the spec to console
	if input.Bool(flagkey.SpecDry) {
		err = opts.env.Validate()
		if err != nil {
			return fv1.AggregateValidationErrors("Environment", err)
		}

		return spec.SpecDry(*opts.env)
	}

	if input.Bool(flagkey.SpecSave) {
		err = opts.env.Validate()
		if err != nil {
			return fv1.AggregateValidationErrors("Environment", err)
		}

		specFile := fmt.Sprintf("env-%v.yaml", opts.env.ObjectMeta.Name)
		err = spec.SpecSave(*opts.env, specFile, false)
		if err != nil {
			return fmt.Errorf("error saving environment spec: %w", err)
		}
		return nil
	}

	opts.env.ObjectMeta.Namespace = currentNS

	_, err = opts.Client().FissionClientSet.CoreV1().Environments(opts.env.Namespace).Create(input.Context(), opts.env, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating resource: %w", err)
	}

	fmt.Printf("environment '%v' created\n", opts.env.ObjectMeta.Name)
	return nil
}

// createEnvironmentFromCmd creates environment initialized with CLI input.
func createEnvironmentFromCmd(input cli.Input) (*fv1.Environment, error) {
	var errs error

	envName := input.String(flagkey.EnvName)
	envImg := input.String(flagkey.EnvImage)

	envBuildCmd := input.String(flagkey.EnvBuildcommand)
	envExternalNetwork := input.Bool(flagkey.EnvExternalNetwork)
	keepArchive := input.Bool(flagkey.EnvKeeparchive)
	envGracePeriod := input.Int64(flagkey.EnvGracePeriod)
	pullSecret := input.String(flagkey.EnvImagePullSecret)

	envVersion := input.Int(flagkey.EnvVersion)

	if envVersion == 0 {
		envVersion = 1
	}

	if !input.IsSet(flagkey.EnvPoolsize) {
		console.Info("poolsize setting default to 3")
	}

	poolsize := input.Int(flagkey.EnvPoolsize)
	if poolsize < 1 {
		console.Warn("poolsize is not positive, if you are using pool manager please set positive value")
	}

	envBuilderImg := input.String(flagkey.EnvBuilderImage)
	if len(envBuilderImg) > 0 {
		if len(envBuildCmd) == 0 {
			envBuildCmd = "build"
		}
	}

	builderEnvParams := input.StringSlice(flagkey.EnvBuilder)
	builderEnvList := util.GetEnvVarFromStringSlice(builderEnvParams)

	runtimeEnvParams := input.StringSlice(flagkey.EnvRuntime)
	runtimeEnvList := util.GetEnvVarFromStringSlice(runtimeEnvParams)

	resourceReq, err := util.GetResourceReqs(input, nil)
	if err != nil {
		errs = errors.Join(errs, err)
	}

	if errs != nil {
		return nil, errs
	}

	env := &fv1.Environment{
		TypeMeta: metav1.TypeMeta{
			Kind:       fv1.CRD_NAME_ENVIRONMENT,
			APIVersion: fv1.CRD_VERSION,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: envName,
		},
		Spec: fv1.EnvironmentSpec{
			Version: envVersion,
			Runtime: fv1.Runtime{
				Image: envImg,
				Container: &apiv1.Container{
					Name: envName,
					Env:  runtimeEnvList,
				},
				PodSpec: &apiv1.PodSpec{
					Containers: []apiv1.Container{
						{
							Name: envName,
						},
					},
				},
			},
			Builder: fv1.Builder{
				Image:   envBuilderImg,
				Command: envBuildCmd,
				Container: &apiv1.Container{
					Name: fv1.BuilderContainerName,
					Env:  builderEnvList,
				},
				PodSpec: &apiv1.PodSpec{
					Containers: []apiv1.Container{
						{
							Name: fv1.BuilderContainerName,
						},
					},
				},
			},
			Poolsize:                     poolsize,
			Resources:                    *resourceReq,
			AllowAccessToExternalNetwork: envExternalNetwork,
			TerminationGracePeriod:       envGracePeriod,
			KeepArchive:                  keepArchive,
			ImagePullSecret:              pullSecret,
		},
	}

	err = util.ApplyLabelsAndAnnotations(input, &env.ObjectMeta)
	if err != nil {
		return nil, err
	}

	return env, nil
}
