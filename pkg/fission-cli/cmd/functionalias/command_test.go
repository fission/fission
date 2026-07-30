// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findSubCommand returns the named direct child of Commands(), failing the
// test if it isn't registered.
func findSubCommand(t *testing.T, name string) *cobra.Command {
	t.Helper()
	for _, c := range Commands().Commands() {
		if c.Name() == name {
			return c
		}
	}
	require.Failf(t, "subcommand not registered", "%q subcommand should be registered", name)
	return nil
}

// TestAliasUpdateHelpCrossReferencesRollback guards the UX-review breadcrumb
// (F12): a user reaching for `alias update` to undo a change should be
// pointed at `fn rollback`, which uses the alias's own Status.History
// instead of requiring the caller to name a target version by hand.
func TestAliasUpdateHelpCrossReferencesRollback(t *testing.T) {
	update := findSubCommand(t, "update")
	assert.Contains(t, update.Long, "fission fn rollback")
}

// TestAliasWaitHelpDocumentsResolvedCondition guards the UX-review fix
// (F10): the shared --for flag's Usage text uses a generic
// "condition=Ready" example that is misleading for FunctionAlias (whose only
// condition type is Resolved, not Ready). Since that flag Usage is shared
// across every `wait` subcommand and can't be overridden per-command, the
// fix lives in `alias wait`'s own Long text instead.
func TestAliasWaitHelpDocumentsResolvedCondition(t *testing.T) {
	wait := findSubCommand(t, "wait")
	assert.Contains(t, wait.Long, "condition=Resolved")
}
