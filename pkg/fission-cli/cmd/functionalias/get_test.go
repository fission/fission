// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// TestAliasGetOmitsHistoryBlockWhenEmpty guards `alias get` output for an
// alias that has never repointed: no HISTORY block at all, so existing
// output is byte-identical to before F13.
func TestAliasGetOmitsHistoryBlockWhenEmpty(t *testing.T) {
	setAliasClient(newAlias()) // no Status.History

	in := dummy.TestFlagSet()
	in.SetString(flagkey.AliasName, "prod")

	out := captureStdout(t, func() error { return Get(in) })
	assert.NotContains(t, out, "HISTORY:")
}

// TestAliasGetRendersHistoryBlockMostRecentLast guards the UX-review fix
// (F13): `alias get` renders a HISTORY block from Status.History, in the
// field's own most-recent-last order.
func TestAliasGetRendersHistoryBlockMostRecentLast(t *testing.T) {
	alias := newAlias()
	alias.Status.History = []fv1.AliasTargetRecord{
		{Version: "hello-v1", SwitchedAt: metav1.Now()},
		{Version: "hello-v2", SwitchedAt: metav1.Now()},
	}
	setAliasClient(alias)

	in := dummy.TestFlagSet()
	in.SetString(flagkey.AliasName, "prod")

	out := captureStdout(t, func() error { return Get(in) })
	require.Contains(t, out, "HISTORY:")
	require.Contains(t, out, "VERSION")
	require.Contains(t, out, "SWITCHED-AT")

	v1Idx := indexOf(t, out, "hello-v1")
	v2Idx := indexOf(t, out, "hello-v2")
	assert.Less(t, v1Idx, v2Idx, "the most recently superseded target (hello-v2) must render last")
}

// TestAliasGetWideOutputOmitsHistoryBlock guards F13's "wide/json unchanged"
// constraint: -o wide must not gain the HISTORY block.
func TestAliasGetWideOutputOmitsHistoryBlock(t *testing.T) {
	alias := newAlias()
	alias.Status.History = []fv1.AliasTargetRecord{{Version: "hello-v1", SwitchedAt: metav1.Now()}}
	setAliasClient(alias)

	in := dummy.TestFlagSet()
	in.SetString(flagkey.AliasName, "prod")
	in.SetString(flagkey.Output, "wide")

	out := captureStdout(t, func() error { return Get(in) })
	assert.NotContains(t, out, "HISTORY:")
}

// TestAliasGetJSONOutputOmitsHistoryBlock guards F13's "wide/json unchanged"
// constraint: -o json must not gain the HISTORY block (it already returns
// via util.PrintStructured before the table/HISTORY code is reached).
func TestAliasGetJSONOutputOmitsHistoryBlock(t *testing.T) {
	alias := newAlias()
	alias.Status.History = []fv1.AliasTargetRecord{{Version: "hello-v1", SwitchedAt: metav1.Now()}}
	setAliasClient(alias)

	in := dummy.TestFlagSet()
	in.SetString(flagkey.AliasName, "prod")
	in.SetString(flagkey.Output, "json")

	out := captureStdout(t, func() error { return Get(in) })
	assert.NotContains(t, out, "HISTORY:")
}

func indexOf(t *testing.T, s, substr string) int {
	t.Helper()
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	t.Fatalf("%q not found in %q", substr, s)
	return -1
}
