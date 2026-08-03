// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecutorRolloutPosture pins the two chart defaults that make poolmgr's
// warm pods survive a helm upgrade (RFC-0028 mechanism A and C):
//
//   - strategy Recreate — the executor is a single-writer control plane keyed
//     on its instance ID; the k8s default RollingUpdate surges a SECOND
//     executor with a different ID and no leader election, and the incoming
//     one's cleanup deletes the objects the outgoing one is still stamping.
//   - adoptExistingResources — with adoption off, startup cleanup deletes
//     every pod whose instance annotation belongs to the previous executor,
//     which is exactly the specialized warm pods and only them.
//
// Rendered with a non-default release name: guards that assert defaults under
// a name where legacy and standard formats coincide pin nothing.
func TestExecutorRolloutPosture(t *testing.T) {
	deployment := func(docs []map[string]any) map[string]any {
		d := find(docs, "Deployment", "executor")
		require.NotNil(t, d, "executor Deployment must render")
		return d
	}

	t.Run("defaults: Recreate + adoption on", func(t *testing.T) {
		docs := render(t)
		d := deployment(docs)

		strategy, _ := d["spec"].(map[string]any)["strategy"].(map[string]any)
		assert.Equal(t, "Recreate", strategy["type"],
			"the default must be Recreate — RollingUpdate rolls two executors with different instance IDs and no lease")

		env := containerEnv(t, d)
		assert.Equal(t, "true", env["ADOPT_EXISTING_RESOURCES"],
			"adoption must default on, or every executor restart deletes the specialized warm pods")
	})

	t.Run("both remain overridable", func(t *testing.T) {
		docs := render(t,
			"--set", "executor.adoptExistingResources=false",
			"--set", "executor.strategy.type=RollingUpdate",
			"--set-string", "executor.strategy.rollingUpdate.maxUnavailable=1")
		d := deployment(docs)

		strategy, _ := d["spec"].(map[string]any)["strategy"].(map[string]any)
		assert.Equal(t, "RollingUpdate", strategy["type"], "the HA escape hatch must stay open")

		env := containerEnv(t, d)
		assert.Equal(t, "false", env["ADOPT_EXISTING_RESOURCES"])
	})
}
