// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/fission/fission/pkg/fission-cli/cmd"
)

// TestCommandTreeLeavesAreRunnable guards the SubCommand factory migration: every
// leaf command (one with no children) must have a RunE wired, otherwise running
// it would silently print help instead of executing. A command that was
// converted to wrapper.SubCommand but lost its action would be caught here.
func TestCommandTreeLeavesAreRunnable(t *testing.T) {
	root := App(cmd.ClientOptions{})

	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		children := c.Commands()
		if len(children) == 0 {
			// Leaf command: must be executable.
			if c.RunE == nil && c.Run == nil {
				t.Errorf("leaf command %q has no Run/RunE", path)
			}
			return
		}
		for _, child := range children {
			walk(child, strings.TrimSpace(path+" "+child.Name()))
		}
	}
	walk(root, root.Name())
}

// TestExpectedCommandsPresent checks a representative set of command paths still
// exists after the command.go refactor, so an accidental drop is caught.
func TestExpectedCommandsPresent(t *testing.T) {
	root := App(cmd.ClientOptions{})

	paths := [][]string{
		{"function", "create"}, {"function", "list"}, {"function", "getmeta"},
		{"package", "info"}, {"package", "list"},
		{"httptrigger", "get"}, {"httptrigger", "list"},
		{"environment", "list"}, {"canary", "list"},
		{"timetrigger", "list"}, {"mqtrigger", "list"}, {"watch", "list"},
		{"spec", "apply"}, {"spec", "destroy"}, {"spec", "validate"},
	}
	for _, p := range paths {
		c, _, err := root.Find(p)
		if err != nil || c == nil || c.Name() != p[len(p)-1] {
			t.Errorf("command %q not found (err=%v)", strings.Join(p, " "), err)
		}
	}
}
