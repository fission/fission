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
