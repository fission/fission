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

package cobra

import (
	"fmt"
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

// Parse is only for converting urfave *cli.Context to Input and will be removed in future.
func Parse(cmd *cobra.Command, args []string) wCli.Input {
	return Cli{c: cmd, args: args}
}

func Wrapper(action cmd.CommandAction) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		return action(Cli{c: c, args: args})
	}
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
			cmd.Flags().MarkDeprecated(f.Name, usage)
		} else if f.Hidden {
			cmd.Flags().MarkHidden(f.Name)
		}
	}
}

func requiredFlags(cmd *cobra.Command, flags ...flag.Flag) {
	for _, f := range flags {
		toCobraFlag(cmd, f, false)
		cmd.MarkFlagRequired(f.Name)
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

func WrapperChain(actions ...cmd.CommandAction) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		for _, action := range actions {
			err := action(Cli{c: c, args: args})
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func (u Cli) Args(index int) string {
	if len(u.args) < index+1 {
		return ""
	}
	return u.args[index]
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
	vals := make([]int64, len(v))
	for _, i := range v {
		vals = append(vals, int64(i))
	}
	return vals
}

func (u Cli) GlobalBool(key string) bool {
	v, _ := u.c.Flags().GetBool(key)
	return v
}

func (u Cli) GlobalString(key string) string {
	v, _ := u.c.Flags().GetString(key)
	return v
}

func (u Cli) GlobalStringSlice(key string) []string {
	v, _ := u.c.Flags().GetStringArray(key)
	return v
}

func (u Cli) GlobalInt(key string) int {
	v, _ := u.c.Flags().GetInt(key)
	return v
}

func (u Cli) GlobalIntSlice(key string) []int {
	v, _ := u.c.Flags().GetIntSlice(key)
	return v
}

func (u Cli) GlobalInt64(key string) int64 {
	v, _ := u.c.Flags().GetInt64(key)
	return v
}

func (u Cli) GlobalInt64Slice(key string) []int64 {
	v, _ := u.c.Flags().GetInt64Slice(key)
	return v
}

func (u Cli) Duration(key string) time.Duration {
	v, _ := u.c.Flags().GetDuration(key)
	return v
}
