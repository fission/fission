// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })
	os.Stdout = w
	require.NoError(t, fn())
	w.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

// TestAliasDeleteOutputMatchesFnDeleteStyle guards the UX-review fix (F11):
// `alias delete` used to print "function alias 'name.namespace' deleted",
// diverging from `fn delete`'s "function 'name' deleted" form. Aligning on
// the bare-name form keeps the two delete commands' output consistent.
func TestAliasDeleteOutputMatchesFnDeleteStyle(t *testing.T) {
	fc := setAliasClient(newAlias()) // name=prod, namespace=default

	in := dummy.TestFlagSet()
	in.SetString(flagkey.AliasName, "prod")

	out := captureStdout(t, func() error { return Delete(in) })
	assert.Equal(t, "function alias 'prod' deleted\n", out)

	_, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	assert.Error(t, err, "the alias must actually be gone")
}

func TestAliasDeleteIgnoreNotFound(t *testing.T) {
	setAliasClient() // empty clientset

	in := dummy.TestFlagSet()
	in.SetString(flagkey.AliasName, "absent")
	in.SetBool(flagkey.IgnoreNotFound, true)

	require.NoError(t, Delete(in))
}

func TestAliasDeleteMissingAliasErrors(t *testing.T) {
	setAliasClient() // empty clientset

	in := dummy.TestFlagSet()
	in.SetString(flagkey.AliasName, "absent")

	require.Error(t, Delete(in))
}
