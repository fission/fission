// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
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

// names returns the Name of every env var, in order.
func envNames(vars []apiv1.EnvVar) []string {
	out := make([]string, len(vars))
	for i, v := range vars {
		out[i] = v.Name
	}
	return out
}

func TestSidecarAPIEnvVars(t *testing.T) {
	t.Run("both features off: nil, pods stay byte-identical", func(t *testing.T) {
		t.Setenv("STATESVC_URL", "")
		t.Setenv("AGENT_RUNTIME_URL", "")
		assert.Empty(t, SidecarAPIEnvVars("/userfunc"))
	})

	t.Run("only agent on: agent vars only", func(t *testing.T) {
		t.Setenv("STATESVC_URL", "")
		t.Setenv("AGENT_RUNTIME_URL", "http://agentruntime.fission:8894")
		assert.Equal(t, []string{"FISSION_AGENT_RUNTIME_URL", "FISSION_AGENT_TOKEN_PATH"}, envNames(SidecarAPIEnvVars("/userfunc")))
	})

	t.Run("both on: state group precedes agent group", func(t *testing.T) {
		t.Setenv("STATESVC_URL", "http://statesvc.fission")
		t.Setenv("AGENT_RUNTIME_URL", "http://agentruntime.fission:8894")
		assert.Equal(t, []string{
			"FISSION_STATE_URL", "FISSION_STATE_TOKEN_PATH",
			"FISSION_AGENT_RUNTIME_URL", "FISSION_AGENT_TOKEN_PATH",
		}, envNames(SidecarAPIEnvVars("/userfunc")))
	})
}
