// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// TestExecutorRolloutPosture pins the two chart defaults that make poolmgr's
// warm pods survive a helm upgrade (RFC-0028 mechanism A and C):
//
//   - overlap-free rollout (maxSurge 0 / maxUnavailable 1) — the executor is
//     a single-writer control plane keyed on its instance ID; the k8s default
//     surges a SECOND executor with a different ID and no leader election,
//     and the incoming one's cleanup deletes the objects the outgoing one is
//     still stamping. Expressed within RollingUpdate, NOT type Recreate: a
//     live Deployment carries a defaulted rollingUpdate block, and the API
//     forbids it alongside Recreate — flipping the type fails every real
//     helm upgrade ("spec.strategy.rollingUpdate: Forbidden", found the hard
//     way by the upgrade gate).
//   - adoptExistingResources — with adoption off, startup cleanup deletes
//     every pod whose instance annotation belongs to the previous executor,
//     which is exactly the specialized warm pods and only them.
func TestExecutorRolloutPosture(t *testing.T) {
	executor := func(docs manifests) *appsv1.Deployment {
		return deployment(t, docs, "executor")
	}

	t.Run("defaults: overlap-free rollout + adoption on", func(t *testing.T) {
		docs := render(t)
		d := executor(docs)

		strategy := d.Spec.Strategy
		assert.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, strategy.Type,
			"the type must stay RollingUpdate — flipping to Recreate fails API validation against every live install")
		require.NotNil(t, strategy.RollingUpdate)
		assert.Equal(t, intstr.FromInt32(0), *strategy.RollingUpdate.MaxSurge,
			"maxSurge 0 is the single-writer guarantee: never two executors at once")
		assert.Equal(t, intstr.FromInt32(1), *strategy.RollingUpdate.MaxUnavailable,
			"maxUnavailable 1 lets the old pod die before the new one starts")

		env := containerEnv(t, d)
		assert.Equal(t, "true", env["ADOPT_EXISTING_RESOURCES"],
			"adoption must default on, or every executor restart deletes the specialized warm pods")
	})

	t.Run("overrides that DIFFER from the defaults are honored", func(t *testing.T) {
		// A --set restating a default pins nothing. Render the documented HA
		// posture (surge with a standby) and the adoption opt-out.
		docs := render(t,
			"--set", "executor.adoptExistingResources=false",
			"--set", "executor.strategy.rollingUpdate.maxSurge=1",
			"--set", "executor.strategy.rollingUpdate.maxUnavailable=0")
		d := executor(docs)

		ru := d.Spec.Strategy.RollingUpdate
		require.NotNil(t, ru)
		assert.Equal(t, intstr.FromInt32(1), *ru.MaxSurge, "the HA surge escape hatch must stay open")
		assert.Equal(t, intstr.FromInt32(0), *ru.MaxUnavailable)

		env := containerEnv(t, d)
		assert.Equal(t, "false", env["ADOPT_EXISTING_RESOURCES"])
	})

	// The opt-out RELEASES.md advertises must render a VALID Deployment: the
	// API rejects a rollingUpdate block alongside Recreate, and helm --set
	// MERGES into the default's rollingUpdate — the template must drop the
	// block when the type is Recreate, or the opt-out is a hard upgrade
	// failure (the exact trap the upgrade gate caught when Recreate was
	// briefly the default).
	t.Run("Recreate opt-out renders a valid strategy", func(t *testing.T) {
		d := executor(render(t, "--set", "executor.strategy.type=Recreate"))
		assert.Equal(t, appsv1.RecreateDeploymentStrategyType, d.Spec.Strategy.Type)
		assert.Nil(t, d.Spec.Strategy.RollingUpdate,
			"Recreate + rollingUpdate is rejected by the API server")
	})

	// Clearing the strategy entirely must also work (k8s default applies).
	t.Run("null strategy renders no strategy block", func(t *testing.T) {
		d := executor(render(t, "--set", "executor.strategy=null"))
		assert.Equal(t, appsv1.DeploymentStrategy{}, d.Spec.Strategy,
			"a nulled strategy must fall back to the Kubernetes default")
	})
}
