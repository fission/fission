// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStateCredentialsJSONWireCompat is a compat gate for the json/v2
// migration — this emission is a cross-language contract: StateCredentials is
// JSON-encoded by writeStateTokenFile (statetoken.go, ~line 53) and written
// to StateTokenFileName on the shared pod volume, where language SDKs in
// OTHER languages (not just Go) read it. If this fails after a migration
// commit, add compat options at the marshal site in statetoken.go, do not
// update the fixture.
func TestStateCredentialsJSONWireCompat(t *testing.T) {
	t.Parallel()

	t.Run("fully populated", func(t *testing.T) {
		t.Parallel()

		creds := StateCredentials{
			Namespace: "default",
			Keyspace:  "myfunc-state",
			Token:     "dev-unauthenticated",
		}
		const want = `{"namespace":"default","keyspace":"myfunc-state","token":"dev-unauthenticated"}`

		got, err := json.Marshal(creds)
		require.NoError(t, err)
		require.Equal(t, want, string(got))

		var back StateCredentials
		require.NoError(t, json.Unmarshal(got, &back))
		assert.Equal(t, creds, back)
	})

	t.Run("zero-heavy", func(t *testing.T) {
		t.Parallel()

		// None of StateCredentials' fields carry an omitempty tag, so an
		// all-empty-string instance still emits every key with an empty
		// string value (never omitted, never null) — pin that today no field
		// disappears from the object for a language SDK parsing it.
		creds := StateCredentials{}
		const want = `{"namespace":"","keyspace":"","token":""}`

		got, err := json.Marshal(creds)
		require.NoError(t, err)
		require.Equal(t, want, string(got))

		var back StateCredentials
		require.NoError(t, json.Unmarshal(got, &back))
		assert.Equal(t, creds, back)
	})
}
