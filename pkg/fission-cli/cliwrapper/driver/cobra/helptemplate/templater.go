/*
Copyright 2016 The Kubernetes Authors.

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

// Original file location: https://github.com/kubernetes/kubectl/tree/master/pkg/util/templates

package helptemplate

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/template"
	"unicode"

	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
)

const AliasSeparator = " |:|: "

type FlagExposer interface {
	ExposeFlags(cmd *cobra.Command, flags ...string) FlagExposer
}

func ActsAsRootCommand(cmd *cobra.Command, filters []string, groups ...CommandGroup) FlagExposer {
	if cmd == nil {
		panic("nil root command")
	}
	templater := &templater{
		RootCmd:       cmd,
		UsageTemplate: MainUsageTemplate(),
		HelpTemplate:  MainHelpTemplate(),
		CommandGroups: groups,
		Filtered:      filters,
	}
	cmd.SetFlagErrorFunc(templater.FlagErrorFunc())
	cmd.SetUsageFunc(templater.UsageFunc())
	cmd.SetHelpFunc(templater.HelpFunc())
	return templater
}

type templater struct {
	UsageTemplate string
	HelpTemplate  string
	RootCmd       *cobra.Command
	CommandGroups
	Filtered []string
}

func (templater *templater) FlagErrorFunc(exposedFlags ...string) func(*cobra.Command, error) error {
	return func(c *cobra.Command, err error) error {
		c.SilenceUsage = true
		return fmt.Errorf("%s\nSee '%s --help' for usage", err, c.CommandPath())
	}
}

func (templater *templater) ExposeFlags(cmd *cobra.Command, flags ...string) FlagExposer {
	cmd.SetUsageFunc(templater.UsageFunc(flags...))
	return templater
}

func (templater *templater) HelpFunc() func(*cobra.Command, []string) {
	return func(c *cobra.Command, s []string) {
		t := template.New("help")
		t.Funcs(templater.templateFuncs())
		template.Must(t.Parse(templater.HelpTemplate))
		err := t.Execute(os.Stdout, c)
		if err != nil {
			c.Println(err)
		}
	}
}

func (templater *templater) UsageFunc(exposedFlags ...string) func(*cobra.Command) error {
	return func(c *cobra.Command) error {
		t := template.New("usage")
		t.Funcs(templater.templateFuncs(exposedFlags...))
		template.Must(t.Parse(templater.UsageTemplate))
		return t.Execute(os.Stdout, c)
	}
}

func (templater *templater) templateFuncs(exposedFlags ...string) template.FuncMap {
	return template.FuncMap{
		"trim":                strings.TrimSpace,
		"trimRight":           func(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) },
		"trimLeft":            func(s string) string { return strings.TrimLeftFunc(s, unicode.IsSpace) },
		"gt":                  cobra.Gt,
		"eq":                  cobra.Eq,
		"rpad":                rpad,
		"appendIfNotPresent":  appendIfNotPresent,
		"flagsNotIntersected": flagsNotIntersected,
		"visibleFlags":        visibleFlags,
		"flagsUsages":         flagsUsages,
		"cmdGroups":           templater.cmdGroups,
		"cmdGroupsString":     templater.cmdGroupsString,
		"rootCmd":             templater.rootCmdName,
		"isRootCmd":           templater.isRootCmd,
		"optionsCmdFor":       templater.optionsCmdFor,
		"usageLine":           templater.usageLine,
		"exposed": func(c *cobra.Command) *flag.FlagSet {
			exposed := flag.NewFlagSet("exposed", flag.ContinueOnError)
			exposed.SortFlags = false
			if len(exposedFlags) > 0 {
				for _, name := range exposedFlags {
					if flag := c.Flags().Lookup(name); flag != nil {
						exposed.AddFlag(flag)
					}
				}
			}
			return exposed
		},
	}
}

func (templater *templater) cmdGroups(c *cobra.Command, all []*cobra.Command) []CommandGroup {
	if len(templater.CommandGroups) > 0 && c == templater.RootCmd {
		all = filter(all, templater.Filtered...)
		return AddAdditionalCommands(templater.CommandGroups, "Other Commands:", all)
	}
	all = filter(all, "options")
	return []CommandGroup{
		{
			Message:  "Available Commands:",
			Commands: all,
		},
	}
}

func (t *templater) cmdGroupsString(c *cobra.Command) string {
	groups := []string{}
	for _, cmdGroup := range t.cmdGroups(c, c.Commands()) {
		cmds := []string{cmdGroup.Message}
		for _, cmd := range cmdGroup.Commands {
			if cmd.IsAvailableCommand() {
				cmds = append(cmds, "  "+rpad(cmd.Name(), cmd.NamePadding())+" "+cmd.Short)
			}
		}
		groups = append(groups, strings.Join(cmds, "\n"))
	}
	return strings.Join(groups, "\n\n")
}

func (t *templater) rootCmdName(c *cobra.Command) string {
	return t.rootCmd(c).CommandPath()
}

func (t *templater) isRootCmd(c *cobra.Command) bool {
	return t.rootCmd(c) == c
}

func (t *templater) parents(c *cobra.Command) []*cobra.Command {
	parents := []*cobra.Command{c}
	for current := c; !t.isRootCmd(current) && current.HasParent(); {
		current = current.Parent()
		parents = append(parents, current)
	}
	return parents
}

func (t *templater) rootCmd(c *cobra.Command) *cobra.Command {
	if c != nil && !c.HasParent() {
		return c
	}
	if t.RootCmd == nil {
		panic("nil root cmd")
	}
	return t.RootCmd
}

func (t *templater) optionsCmdFor(c *cobra.Command) string {
	if !c.Runnable() {
		return ""
	}
	rootCmdStructure := t.parents(c)
	for i := len(rootCmdStructure) - 1; i >= 0; i-- {
		cmd := rootCmdStructure[i]
		if _, _, err := cmd.Find([]string{"options"}); err == nil {
			return cmd.CommandPath() + " options"
		}
	}
	return ""
}

func (t *templater) usageLine(c *cobra.Command) string {
	var useline string
	if c.HasParent() {
		useline = c.Parent().CommandPath() + " " + c.Use
	} else {
		useline = c.Use
	}
	if c.DisableFlagsInUseLine {
		return useline
	}
	if c.HasAvailableFlags() && !strings.Contains(useline, "[options]") {
		useline += " [options]"
	}
	return useline
}

func flagsUsages(f *flag.FlagSet) string {
	x := new(bytes.Buffer)

	var flags [][]string
	var maxPadLength int

	f.VisitAll(func(flag *flag.Flag) {
		if flag.Hidden {
			return
		}

		format := "--%s=%s"
		if flag.Value.Type() == "string" {
			format = "--%s='%s'"
		}

		var usage string
		var aliases []string

		// extract aliases from usage text
		u := strings.Split(flag.Usage, AliasSeparator)

		if len(u) == 1 {
			// means the flag has no aliases
			usage = u[0]
			format = format + " %s"
		} else {
			usage = u[len(u)-1]
			aliases = u[0 : len(u)-1]
			format = format + " (%s)"
		}

		aliasesStr := strings.Join(aliases, ", ")
		flagName := fmt.Sprintf(format, flag.Name, flag.DefValue, aliasesStr)
		flags = append(flags, []string{flagName, usage})

		if len(flagName) > maxPadLength {
			maxPadLength = len(flagName)
		}
	})

	for _, v := range flags {
		name := rpad(v[0], maxPadLength)
		fmt.Fprintf(x, "  %s %s\n", name, toFitUsagePadding(v[1], maxPadLength))
	}

	return x.String()
}

func toFitUsagePadding(u string, pad int) (usage string) {
	us := strings.Split(u, " ")
	length := pad

	for _, chunk := range us {
		if length+len(chunk) > 100 {
			usage = usage + "\n" + lpad(chunk, pad+len(chunk)+4) // 4 for whitespace in format
			length = pad
			continue
		}
		usage = usage + " " + chunk
		length += len(chunk) + 1 // 1 is for whitespace
	}

	return usage
}

func rpad(s string, padding int) string {
	template := fmt.Sprintf("%%-%ds", padding)
	return fmt.Sprintf(template, s)
}

func lpad(s string, padding int) string {
	template := fmt.Sprintf("%%%ds", padding)
	return fmt.Sprintf(template, s)
}

func appendIfNotPresent(s, stringToAppend string) string {
	if strings.Contains(s, stringToAppend) {
		return s
	}
	return s + " " + stringToAppend
}

func flagsNotIntersected(l *flag.FlagSet, r *flag.FlagSet) *flag.FlagSet {
	f := flag.NewFlagSet("notIntersected", flag.ContinueOnError)
	f.SortFlags = false // disable sorting flags
	l.VisitAll(func(flag *flag.Flag) {
		if r.Lookup(flag.Name) == nil {
			f.AddFlag(flag)
		}
	})
	return f
}

func visibleFlags(l *flag.FlagSet) *flag.FlagSet {
	hidden := "help"
	f := flag.NewFlagSet("visible", flag.ContinueOnError)
	f.SortFlags = false // disable sorting flags
	l.VisitAll(func(flag *flag.Flag) {
		if flag.Name != hidden {
			f.AddFlag(flag)
		}
	})
	return f
}

func filter(cmds []*cobra.Command, names ...string) []*cobra.Command {
	out := []*cobra.Command{}
	for _, c := range cmds {
		if c.Hidden {
			continue
		}
		skip := slices.Contains(names, c.Name())
		if skip {
			continue
		}
		out = append(out, c)
	}
	return out
}
