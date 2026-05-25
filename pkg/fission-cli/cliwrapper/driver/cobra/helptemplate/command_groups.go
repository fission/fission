// SPDX-FileCopyrightText: The Kubernetes Authors
//
// SPDX-License-Identifier: Apache-2.0

// Original file location: https://github.com/kubernetes/kubectl/tree/master/pkg/util/templates

package helptemplate

import (
	"slices"

	"github.com/spf13/cobra"
)

type CommandGroup struct {
	Message  string
	Commands []*cobra.Command
}

type CommandGroups []CommandGroup

func (g CommandGroups) Add(c *cobra.Command) {
	for _, group := range g {
		c.AddCommand(group.Commands...)
	}
}

func (g CommandGroups) Has(c *cobra.Command) bool {
	for _, group := range g {
		if slices.Contains(group.Commands, c) {
			return true
		}
	}
	return false
}

func AddAdditionalCommands(g CommandGroups, message string, cmds []*cobra.Command) CommandGroups {
	group := CommandGroup{Message: message}
	for _, c := range cmds {
		// Don't show commands that have no short description
		if !g.Has(c) && len(c.Short) != 0 {
			group.Commands = append(group.Commands, c)
		}
	}
	if len(group.Commands) == 0 {
		return g
	}
	return append(g, group)
}
