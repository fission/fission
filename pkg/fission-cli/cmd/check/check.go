// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package check

import (
	"fmt"

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

	checks := []healthcheck.CategoryID{}

	userProvidedNS, _, err := opts.GetResourceNamespace(input, flagkey.Namespace)
	if err != nil {
		return fmt.Errorf("error retrieving user provided namespace: %w", err)
	}

	if input.IsSet(flagkey.PreCheckOnly) {
		checks = append(checks, healthcheck.Kubernetes)
	} else {
		checks = append(checks, healthcheck.FissionServices, healthcheck.FissionVersion, healthcheck.OCIDelivery)
	}

	hc := healthcheck.NewHealthChecker(opts.Client(), checks, userProvidedNS)

	healthcheck.RunChecks(input.Context(), input, opts.Client(), hc)
	return nil
}
