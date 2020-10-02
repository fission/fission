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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	env *fv1.Environment
}

func Update(input cli.Input) error {
	return (&UpdateSubCommand{}).do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateSubCommand) complete(input cli.Input) error {
	env, err := opts.Client().V1().Environment().Get(&metav1.ObjectMeta{
		Name:      input.String(flagkey.EnvName),
		Namespace: input.String(flagkey.NamespaceEnvironment),
	})
	if err != nil {
		return errors.Wrap(err, "error finding environment")
	}

	env, err = updateExistingEnvironmentWithCmd(env, input)
	if err != nil {
		return err
	}

	opts.env = env
	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	_, err := opts.Client().V1().Environment().Update(opts.env)
	if err != nil {
		return errors.Wrap(err, "error updating environment")
	}

	fmt.Printf("environment '%v' updated\n", opts.env.ObjectMeta.Name)
	return nil
}

// updateExistingEnvironmentWithCmd updates a existing environment's value based on CLI input.
func updateExistingEnvironmentWithCmd(env *fv1.Environment, input cli.Input) (*fv1.Environment, error) {
	e := utils.MultiErrorWithFormat()

	envImg := input.String(flagkey.EnvImage)
	envBuilderImg := input.String(flagkey.EnvBuilderImage)
	envBuildCmd := input.String(flagkey.EnvBuildcommand)
	envExternalNetwork := input.Bool(flagkey.EnvExternalNetwork)
	resourceReq, err := util.GetResourceReqs(input, nil)
	env.Spec.Resources = *resourceReq

	if err != nil {
		e = multierror.Append(e, err)
	}

	if len(envImg) == 0 && len(envBuilderImg) == 0 && len(envBuildCmd) == 0 {
		e = multierror.Append(e, errors.New("need --image to specify env image, or use --builder to specify env builder, or use --buildcmd to specify new build command"))
	}

	if len(envImg) > 0 {
		env.Spec.Runtime.Image = envImg
	}

	if env.Spec.Version == 1 && (len(envBuilderImg) > 0 || len(envBuildCmd) > 0) {
		e = multierror.Append(e, errors.New("version 1 Environments do not support builders. Must specify --version=2"))
	}

	if len(envBuilderImg) > 0 {
		env.Spec.Builder.Image = envBuilderImg
	}
	if len(envBuildCmd) > 0 {
		env.Spec.Builder.Command = envBuildCmd
	}

	if input.IsSet(flagkey.EnvPoolsize) {
		env.Spec.Poolsize = input.Int(flagkey.EnvPoolsize)
	}

	if input.IsSet(flagkey.EnvGracePeriod) {
		env.Spec.TerminationGracePeriod = input.Int64(flagkey.EnvGracePeriod)
	}

	if input.IsSet(flagkey.EnvKeeparchive) {
		env.Spec.KeepArchive = input.Bool(flagkey.EnvKeeparchive)
	}

	if input.IsSet(flagkey.EnvImagePullSecret) {
		env.Spec.ImagePullSecret = input.String(flagkey.EnvImagePullSecret)
	}

	env.Spec.AllowAccessToExternalNetwork = envExternalNetwork

	// TODO: allow to update resource.
	//if input.IsSet(flagkey.RuntimeMincpu) || input.IsSet(flagkey.RuntimeMaxcpu) ||
	//	input.IsSet(flagkey.RuntimeMinmemory) || input.IsSet(flagkey.RuntimeMaxmemory) ||
	//	input.IsSet(flagkey.ReplicasMinscale) || input.IsSet(flagkey.ReplicasMaxscale) {
	//	e = multierror.Append(e, errors.New("updating resource limits/requests for existing environments is currently unsupported; re-create the environment instead"))
	//}

	if e.ErrorOrNil() != nil {
		return nil, e.ErrorOrNil()
	}

	err = env.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Environment", err)
	}

	return env, nil
}
