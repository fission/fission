// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// Helpers for restarting the cluster-wide executor and waiting for the rollout.
// These exist so tests can exercise startup-only executor behaviour — chiefly
// AdoptExistingResources, which runs once per process when
// ADOPT_EXISTING_RESOURCES=true. Restarting the shared executor is disruptive
// to every function in the cluster, so callers must live in the serial suite,
// never the parallel common suite.

const (
	// executorDeploymentName / executorContainerName identify the executor
	// Deployment the Helm chart installs (charts/fission-all/templates/executor/
	// deployment.yaml); both are the literal "executor".
	executorDeploymentName = "executor"
	executorContainerName  = "executor"
	// restartedAtAnnotation mirrors `kubectl rollout restart`: bumping it on the
	// pod template guarantees a rollout even when the env-var value is unchanged.
	restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"
	// defaultFissionNamespace is the chart default and the namespace CI exports
	// as FISSION_NAMESPACE.
	defaultFissionNamespace = "fission"
)

// FissionNamespace returns the namespace the Fission control plane runs in,
// read from FISSION_NAMESPACE and defaulting to "fission". The executor
// Deployment lives here.
func (f *Framework) FissionNamespace() string {
	if ns := os.Getenv("FISSION_NAMESPACE"); ns != "" {
		return ns
	}
	return defaultFissionNamespace
}

// SetExecutorEnv sets an environment variable on the executor Deployment's
// container and rolls the pod. The patch mutates the pod template (and bumps a
// restartedAt annotation), so it always triggers a rollout — even when the
// value is unchanged. It returns the Deployment generation produced by the
// patch (pass it to WaitForExecutorRollout) and a best-effort restore func that
// reverts the variable to the value observed before the patch.
//
// This is how a serial test exercises startup-only executor behaviour such as
// ADOPT_EXISTING_RESOURCES: flip the env var, wait for the new pod, assert.
//
// The patch also forces the Deployment's strategy to Recreate. With the chart
// default (RollingUpdate, maxSurge 25% → 1 with replicas=1) and leader election
// disabled, a rollout briefly runs *two* executors with different instance IDs;
// the outgoing one keeps re-stamping objects with its own ID while the incoming
// one's adopt + cleanup pass runs, so the incoming executor's reaper deletes
// (and the cluster then recreates) the very objects adopt is meant to keep.
// Recreate terminates the old pod before starting the new one, giving the clean
// single-instance restart adopt is designed for.
func (f *Framework) SetExecutorEnv(t *testing.T, ctx context.Context, key, value string) (generation int64, restore func()) {
	t.Helper()
	ns := f.FissionNamespace()

	dep, err := f.kubeClient.AppsV1().Deployments(ns).Get(ctx, executorDeploymentName, metav1.GetOptions{})
	require.NoErrorf(t, err, "SetExecutorEnv: get executor Deployment %s/%s", ns, executorDeploymentName)
	oldValue, hadOld := executorEnvValue(dep, key)

	updated, err := f.patchExecutorEnv(ctx, key, value)
	require.NoErrorf(t, err, "SetExecutorEnv: set %s=%q on executor Deployment", key, value)

	restore = func() {
		// Don't wait for this rollout: nothing runs after the serial suite that
		// depends on the executor being back at its default config — this is
		// only courtesy cleanup so the shared cluster isn't left mutated.
		rctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		restoreVal := oldValue
		if !hadOld {
			restoreVal = "false"
		}
		if _, err := f.patchExecutorEnv(rctx, key, restoreVal); err != nil {
			t.Logf("SetExecutorEnv: restore %s=%q failed: %v", key, restoreVal, err)
		}
	}
	return updated.Generation, restore
}

// patchExecutorEnv strategically merges a single env var (matched by name, the
// env list's merge key) into the executor container, forces the Recreate
// strategy (so no two executors overlap during the rollout — see SetExecutorEnv),
// and bumps the restartedAt annotation to force a rollout. Other env entries and
// containers are left untouched; rollingUpdate is cleared because it is mutually
// exclusive with Recreate.
func (f *Framework) patchExecutorEnv(ctx context.Context, key, value string) (*appsv1.Deployment, error) {
	patch := fmt.Sprintf(
		`{"spec":{"strategy":{"type":"Recreate","rollingUpdate":null},"template":{"metadata":{"annotations":{%q:%q}},"spec":{"containers":[{"name":%q,"env":[{"name":%q,"value":%q}]}]}}}}`,
		restartedAtAnnotation, strconv.FormatInt(time.Now().UnixNano(), 10),
		executorContainerName, key, value,
	)
	return f.kubeClient.AppsV1().Deployments(f.FissionNamespace()).Patch(
		ctx, executorDeploymentName, k8stypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
}

// executorEnvValue returns the current value of env var key on the executor
// container, and whether it was set.
func executorEnvValue(dep *appsv1.Deployment, key string) (string, bool) {
	for i := range dep.Spec.Template.Spec.Containers {
		c := &dep.Spec.Template.Spec.Containers[i]
		if c.Name != executorContainerName {
			continue
		}
		for j := range c.Env {
			if c.Env[j].Name == key {
				return c.Env[j].Value, true
			}
		}
	}
	return "", false
}

// WaitForExecutorRollout blocks until the executor Deployment has fully rolled
// out at generation atLeast or newer: the controller has observed the new
// generation, every replica is updated and available, and no old pods remain.
//
// The executor runs its adopt + cleanup pass *before* it reports ready
// (cachesSynced is set after runAdoptCleanup returns, and /readyz gates on it,
// see pkg/executor), so a completed rollout means the adopt pass has run.
func (f *Framework) WaitForExecutorRollout(t *testing.T, ctx context.Context, atLeast int64, timeout time.Duration) {
	t.Helper()
	ns := f.FissionNamespace()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		dep, err := f.kubeClient.AppsV1().Deployments(ns).Get(ctx, executorDeploymentName, metav1.GetOptions{})
		if !assert.NoErrorf(c, err, "get executor Deployment") {
			return
		}
		want := int32(1)
		if dep.Spec.Replicas != nil {
			want = *dep.Spec.Replicas
		}
		st := dep.Status
		assert.GreaterOrEqualf(c, st.ObservedGeneration, atLeast,
			"executor rollout not yet observed (observed %d, want >= %d)", st.ObservedGeneration, atLeast)
		assert.Equalf(c, want, st.UpdatedReplicas, "executor updated replicas (rollout in progress?)")
		assert.Equalf(c, want, st.AvailableReplicas, "executor available replicas")
		assert.Equalf(c, want, st.Replicas, "executor total replicas (old pod still terminating?)")
		assert.Zerof(c, st.UnavailableReplicas, "executor unavailable replicas")
	}, timeout, 2*time.Second)
}
