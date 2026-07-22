// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateAPIEnvVars(t *testing.T) {
	t.Run("feature off: nil, pods stay byte-identical", func(t *testing.T) {
		t.Setenv("STATESVC_URL", "")
		assert.Nil(t, StateAPIEnvVars("/userfunc"))
	})

	t.Run("feature on: url and token path", func(t *testing.T) {
		t.Setenv("STATESVC_URL", "http://statesvc.fission:8893")
		vars := StateAPIEnvVars("/userfunc")
		require.Len(t, vars, 2)
		assert.Equal(t, "FISSION_STATE_URL", vars[0].Name)
		assert.Equal(t, "http://statesvc.fission:8893", vars[0].Value)
		assert.Equal(t, "FISSION_STATE_TOKEN_PATH", vars[1].Name)
		assert.Equal(t, "/userfunc/.fission-state-token", vars[1].Value)
	})
}
