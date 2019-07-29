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

	"github.com/hashicorp/go-multierror"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/generator"
)

type UpdateSubCommand struct {
	client    *client.Client
	env       *fv1.Environment
	generator generator.StructuredGenerator
}

func Update(flags cli.Input) error {
	opts := UpdateSubCommand{
		client: cmd.GetServer(flags),
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
	m, err := cmd.GetMetadata(flags)
	if err != nil {
		return err
	}

	env, err := opts.client.EnvironmentGet(m)
	util.CheckErr(err, "find environment")

	env, err = updateExistingEnvironmentWithCmd(env, flags)
	if err != nil {
		return err
	}

	opts.env = env
	return nil
}

func (opts *UpdateSubCommand) run(flags cli.Input) error {
	_, err := opts.client.EnvironmentUpdate(opts.env)
	util.CheckErr(err, "update environment")

	fmt.Printf("environment '%v' updated\n", opts.env.Metadata.Name)
	return nil
}

// updateExistingEnvironmentWithCmd updates a existing environment's value based on CLI input.
func updateExistingEnvironmentWithCmd(env *fv1.Environment, flags cli.Input) (*fv1.Environment, error) {
	e := &multierror.Error{}

	envImg := flags.String(cmd.ENVIRONMENT_IMAGE)
	envBuilderImg := flags.String(cmd.ENVIRONMENT_BUILDER)
	envBuildCmd := flags.String(cmd.ENVIRONMENT_BUILDCOMMAND)
	envExternalNetwork := flags.Bool(cmd.ENVIRONMENT_EXTERNAL_NETWORK)

	if len(envImg) == 0 && len(envBuilderImg) == 0 && len(envBuildCmd) == 0 {
		e = multierror.Append(e, errors.New("need --image to specify env image, or use --builder to specify env builder, or use --buildcmd to specify new build command."))
	}

	if len(envImg) > 0 {
		env.Spec.Runtime.Image = envImg
	}

	if env.Spec.Version == 1 && (len(envBuilderImg) > 0 || len(envBuildCmd) > 0) {
		e = multierror.Append(e, errors.New("version 1 Environments do not support builders. Must specify --version=2."))
	}

	if len(envBuilderImg) > 0 {
		env.Spec.Builder.Image = envBuilderImg
	}
	if len(envBuildCmd) > 0 {
		env.Spec.Builder.Command = envBuildCmd
	}

	if flags.IsSet(cmd.ENVIRONMENT_POOLSIZE) {
		env.Spec.Poolsize = flags.Int(cmd.ENVIRONMENT_POOLSIZE)
	}

	if flags.IsSet(cmd.ENVIRONMENT_GRACE_PERIOD) {
		env.Spec.TerminationGracePeriod = flags.Int64(cmd.ENVIRONMENT_GRACE_PERIOD)
	}

	if flags.IsSet(cmd.ENVIRONMENT_KEEPARCHIVE) {
		env.Spec.KeepArchive = flags.Bool(cmd.ENVIRONMENT_KEEPARCHIVE)
	}

	env.Spec.AllowAccessToExternalNetwork = envExternalNetwork

	if flags.IsSet(cmd.RUNTIME_MINCPU) || flags.IsSet(cmd.RUNTIME_MAXCPU) ||
		flags.IsSet(cmd.RUNTIME_MINMEMORY) || flags.IsSet(cmd.RUNTIME_MAXMEMORY) ||
		flags.IsSet(cmd.RUNTIME_MINSCALE) || flags.IsSet(cmd.RUNTIME_MAXSCALE) {
		e = multierror.Append(e, errors.New("updating resource limits/requests for existing environments is currently unsupported; re-create the environment instead"))
	}

	if e.ErrorOrNil() != nil {
		return nil, e.ErrorOrNil()
	}

	return env, nil
}
