// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cobra

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	wCli "github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra/helptemplate"
	cmd "github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

var _ wCli.Input = &Cli{}

type (
	Cli struct {
		c    *cobra.Command
		args []string
	}
)

func Wrapper(action cmd.CommandAction) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		return action(Cli{c: c, args: args})
	}
}

// SubCommand wires a Fission CommandAction and its flag set onto c, returning c
// for convenient inline use. It collapses the "build cobra.Command with
// RunE: Wrapper(action), then call SetFlags" pair repeated for every leaf
// command in the per-resource command.go files, guaranteeing the two stay in
// sync. Callers pass a cobra.Command carrying the descriptive fields (Use,
// Short, Long, Aliases, ...) so nothing about command definition is lost.
func SubCommand(c *cobra.Command, action cmd.CommandAction, flags flag.FlagSet) *cobra.Command {
	c.RunE = Wrapper(action)
	SetFlags(c, flags)
	return c
}

func SetFlags(cmd *cobra.Command, flagSet flag.FlagSet) {
	aliases := make(map[string]string)

	// set global flags
	for _, f := range flagSet.Global {
		globalFlags(cmd, f)
		for _, alias := range f.Aliases {
			aliases[alias] = f.Name
		}
	}

	// set required flags
	for _, f := range flagSet.Required {
		requiredFlags(cmd, f)
		for _, alias := range f.Aliases {
			aliases[alias] = f.Name
		}
	}

	// set optional flags
	for _, f := range flagSet.Optional {
		optionalFlags(cmd, f)
		for _, alias := range f.Aliases {
			aliases[alias] = f.Name
		}
	}

	// set flag alias normalize function
	cmd.Flags().SetNormalizeFunc(
		func(f *pflag.FlagSet, name string) pflag.NormalizedName {
			n, ok := aliases[name]
			if ok {
				name = n
			}
			return pflag.NormalizedName(name)
		},
	)
	cmd.Flags().SortFlags = false
}

func optionalFlags(cmd *cobra.Command, flags ...flag.Flag) {
	for _, f := range flags {
		toCobraFlag(cmd, f, false)
		if f.Deprecated {
			usage := fmt.Sprintf("Use --%v instead. The flag still works for now and will be removed in future", f.Substitute)
			cmd.Flags().MarkDeprecated(f.Name, usage) //nolint: errCheck
		} else if f.Hidden {
			cmd.Flags().MarkHidden(f.Name) //nolint: errCheck
		}
	}
}

func requiredFlags(cmd *cobra.Command, flags ...flag.Flag) {
	for _, f := range flags {
		toCobraFlag(cmd, f, false)
		cmd.MarkFlagRequired(f.Name) //nolint: errCheck
	}
}

func globalFlags(cmd *cobra.Command, flags ...flag.Flag) {
	for _, f := range flags {
		toCobraFlag(cmd, f, true)
	}
}

func toCobraFlag(cmd *cobra.Command, f flag.Flag, global bool) {
	// Workaround to pass aliases to templater for generating flag aliases.
	if len(f.Aliases) > 0 || len(f.Short) > 0 {
		var aliases []string
		if len(f.Short) > 0 {
			f.Aliases = append(f.Aliases, f.Short)
		}
		for _, alias := range f.Aliases {
			dash := "--"
			if len(alias) == 1 {
				dash = "-"
				f.Short = alias
			}
			aliases = append(aliases, dash+alias)
		}
		// Use separator to separator aliases and usage text.
		f.Usage = fmt.Sprintf("%s%s%s",
			strings.Join(aliases, helptemplate.AliasSeparator),
			helptemplate.AliasSeparator, f.Usage)
	}

	flagset := cmd.Flags()
	if global {
		flagset = cmd.PersistentFlags()
	}

	switch f.Type {
	case flag.Bool:
		val, ok := f.DefaultValue.(bool)
		if !ok {
			val = false
		}
		flagset.BoolP(f.Name, f.Short, val, f.Usage)
	case flag.String:
		val, ok := f.DefaultValue.(string)
		if !ok {
			val = ""
		}
		flagset.StringP(f.Name, f.Short, val, f.Usage)
	case flag.StringSlice:
		val, ok := f.DefaultValue.([]string)
		if !ok {
			val = []string{}
		}
		flagset.StringArrayP(f.Name, f.Short, val, f.Usage)
	case flag.Int:
		val, ok := f.DefaultValue.(int)
		if !ok {
			val = 0
		}
		flagset.IntP(f.Name, f.Short, val, f.Usage)
	case flag.IntSlice:
		val, ok := f.DefaultValue.([]int)
		if !ok {
			val = []int{}
		}
		flagset.IntSliceP(f.Name, f.Short, val, f.Usage)
	case flag.Int64:
		val, ok := f.DefaultValue.(int64)
		if !ok {
			val = 0
		}
		flagset.Int64P(f.Name, f.Short, val, f.Usage)
	case flag.Int64Slice:
		val, ok := f.DefaultValue.([]int64)
		if !ok {
			val = []int64{}
		}
		flagset.Int64SliceP(f.Name, f.Short, val, f.Usage)
	case flag.Float32:
		val, ok := f.DefaultValue.(float32)
		if !ok {
			val = 0
		}
		flagset.Float32P(f.Name, f.Short, val, f.Usage)
	case flag.Float64:
		val, ok := f.DefaultValue.(float64)
		if !ok {
			val = 0
		}
		flagset.Float64P(f.Name, f.Short, val, f.Usage)
	case flag.Duration:
		val, ok := f.DefaultValue.(time.Duration)
		if !ok {
			val = 0
		}
		flagset.DurationP(f.Name, f.Short, val, f.Usage)
	}
}

func (u Cli) Context() context.Context {
	return u.c.Context()
}

func (u Cli) IsSet(key string) bool {
	return u.c.Flags().Changed(key)
}

func (u Cli) Bool(key string) bool {
	// TODO: ignore the error here, but we should handle it properly in some ways.
	v, _ := u.c.Flags().GetBool(key)
	return v
}

func (u Cli) String(key string) string {
	v, _ := u.c.Flags().GetString(key)
	return v
}

func (u Cli) StringSlice(key string) []string {
	// difference between StringSlice and StringArray
	// --ss="one" --ss="two,three"
	// StringSlice* - will result in []string{"one", "two", "three"}
	// StringArray* - will result in []s
	// https://github.com/spf13/cobra/issues/661#issuecomment-377684634
	// Use StringArray here to fit our use case.
	v, _ := u.c.Flags().GetStringArray(key)
	return v
}

func (u Cli) Int(key string) int {
	v, _ := u.c.Flags().GetInt(key)
	return v
}

func (u Cli) IntSlice(key string) []int {
	v, _ := u.c.Flags().GetIntSlice(key)
	return v
}

func (u Cli) Int64(key string) int64 {
	v, _ := u.c.Flags().GetInt64(key)
	return v
}

func (u Cli) Int64Slice(key string) []int64 {
	v, _ := u.c.Flags().GetIntSlice(key)
	vals := make([]int64, 0, len(v))
	for _, i := range v {
		vals = append(vals, int64(i))
	}
	return vals
}

func (u Cli) Duration(key string) time.Duration {
	v, _ := u.c.Flags().GetDuration(key)
	return v
}

func (u Cli) Stdout() io.Writer {
	return u.c.OutOrStdout()
}

func (u Cli) Stderr() io.Writer {
	return u.c.OutOrStderr()
}
