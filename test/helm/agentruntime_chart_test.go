// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fission/fission/pkg/svcinfo"
)

// TestAgentRuntimeChartMaxContinuations checks Task 16's chart wiring: the
// agentruntime Deployment carries AGENT_MAX_CONTINUATIONS from
// agentRuntime.maxContinuations, defaulting to 1000 and following an
// explicit override (including the documented 0 = unlimited sentinel).
func TestAgentRuntimeChartMaxContinuations(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "1000", env["AGENT_MAX_CONTINUATIONS"])
	})

	t.Run("override, including 0 = unlimited", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "agentRuntime.maxContinuations=0")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "0", env["AGENT_MAX_CONTINUATIONS"])
	})
}
