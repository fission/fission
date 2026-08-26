// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentAPIEnvVars(t *testing.T) {
	t.Run("feature off: nil, pods stay byte-identical", func(t *testing.T) {
		t.Setenv("AGENT_RUNTIME_URL", "")
		assert.Nil(t, AgentAPIEnvVars("/userfunc"))
	})

	t.Run("feature on: url and token path", func(t *testing.T) {
		t.Setenv("AGENT_RUNTIME_URL", "http://agentruntime.fission:8894")
		vars := AgentAPIEnvVars("/userfunc")
		require.Len(t, vars, 2)
		assert.Equal(t, "FISSION_AGENT_RUNTIME_URL", vars[0].Name)
		assert.Equal(t, "http://agentruntime.fission:8894", vars[0].Value)
		assert.Equal(t, "FISSION_AGENT_TOKEN_PATH", vars[1].Name)
		assert.Equal(t, "/userfunc/.fission-agent-token", vars[1].Value)
	})
}
