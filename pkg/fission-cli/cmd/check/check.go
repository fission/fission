/*
Copyright 2022 The Fission Authors.
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

package check

import (
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/healthcheck"
)

type CheckSubCommand struct {
	cmd.CommandActioner
}

func Check(input cli.Input) error {
	return (&CheckSubCommand{}).do(input)
}

func (opts *CheckSubCommand) do(input cli.Input) error {

	kubeContext := input.String(flagkey.KubeContext)

	checks := []healthcheck.CategoryID{}

	if input.IsSet(flagkey.PreCheckOnly) {
		checks = append(checks, healthcheck.Kubernetes)
	} else {
		checks = append(checks, healthcheck.FissionServices, healthcheck.FissionVersion)
	}

	hc := healthcheck.NewHealthChecker(checks, &healthcheck.Options{
		KubeContext:   kubeContext,
		FissionClient: opts.Client(),
	})

	healthcheck.RunChecks(hc)
	return nil
}
