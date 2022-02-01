package check

import (
	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
	"github.com/spf13/cobra"
)

func Commands() *cobra.Command {
	command := &cobra.Command{
		Use:   "check",
		Short: "Check the fission installation for potential problems",
		Long:  `Check the fission installation for potential problems.`,
		RunE:  wrapper.Wrapper(Check),
	}
	wrapper.SetFlags(command, flag.FlagSet{
		Optional: []flag.Flag{flag.PreCheckOnly},
	})

	return command
}
