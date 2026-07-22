// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
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
	// recreateStrategy switches the executor Deployment to Recreate for the
	// restart (rollingUpdate must be cleared — it's mutually exclusive with
	// Recreate). See SetExecutorEnv for why overlap-free rollout matters.
	recreateStrategy = `{"type":"Recreate","rollingUpdate":null}`
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

// RequireExecutorCan asserts, via a SubjectAccessReview, that the executor's
// ServiceAccount (fission-executor) is allowed verb on the given core/v1
// resource in namespace. It's a deterministic check of the RBAC the adopt path
// depends on — e.g. services `update`, which AdoptExistingResources needs to
// re-stamp a Service in place.
func (f *Framework) RequireExecutorCan(t *testing.T, ctx context.Context, verb, resource, namespace string) {
	t.Helper()
	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: "system:serviceaccount:" + f.FissionNamespace() + ":fission-executor",
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      verb,
				Group:     "",
				Resource:  resource,
			},
		},
	}
	res, err := f.kubeClient.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	require.NoErrorf(t, err, "RequireExecutorCan: SubjectAccessReview for %s %s", verb, resource)
	require.Truef(t, res.Status.Allowed,
		"executor ServiceAccount must be allowed to %s %s in namespace %q (reason: %s)",
		verb, resource, namespace, res.Status.Reason)
}

// SetExecutorEnv sets an environment variable on the executor Deployment's
// container and rolls the pod. The patch mutates the pod template (and bumps a
// restartedAt annotation), so it always triggers a rollout — even when the
// value is unchanged. It returns the Deployment generation produced by the
// patch (pass it to WaitForExecutorRollout) and a best-effort restore func that
// reverts both the variable and the Deployment strategy to what they were
// before the patch.
//
// This is how a serial test exercises startup-only executor behaviour such as
// ADOPT_EXISTING_RESOURCES: flip the env var, wait for the new pod, assert.
//
// The enabling patch forces the Deployment's strategy to Recreate. With the
// chart default (RollingUpdate, maxSurge 25% → 1 with replicas=1) and leader
// election disabled, a rollout briefly runs *two* executors with different
// instance IDs; the outgoing one keeps re-stamping objects with its own ID
// while the incoming one's adopt + cleanup pass runs, so the incoming
// executor's reaper deletes (and the cluster then recreates) the very objects
// adopt is meant to keep. Recreate terminates the old pod before starting the
// new one, giving the clean single-instance restart adopt is designed for. The
// restore func puts the original strategy back.
func (f *Framework) SetExecutorEnv(t *testing.T, ctx context.Context, key, value string) (generation int64, restore func()) {
	t.Helper()
	ns := f.FissionNamespace()

	dep, err := f.kubeClient.AppsV1().Deployments(ns).Get(ctx, executorDeploymentName, metav1.GetOptions{})
	require.NoErrorf(t, err, "SetExecutorEnv: get executor Deployment %s/%s", ns, executorDeploymentName)
	oldValue, hadOld := executorEnvValue(dep, key)
	oldStrategy, err := json.Marshal(dep.Spec.Strategy)
	require.NoErrorf(t, err, "SetExecutorEnv: marshal original strategy")

	updated, err := f.patchExecutorEnv(ctx, key, value, recreateStrategy)
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
		// Restore the original strategy too, not just the env var.
		if _, err := f.patchExecutorEnv(rctx, key, restoreVal, string(oldStrategy)); err != nil {
			t.Logf("SetExecutorEnv: restore %s=%q failed: %v", key, restoreVal, err)
		}
	}
	return updated.Generation, restore
}

// patchExecutorEnv strategically merges a single env var (matched by name, the
// env list's merge key) into the executor container, sets the Deployment
// strategy to strategyJSON, and bumps the restartedAt annotation to force a
// rollout. Other env entries and containers are left untouched.
func (f *Framework) patchExecutorEnv(ctx context.Context, key, value, strategyJSON string) (*appsv1.Deployment, error) {
	patch := fmt.Sprintf(
		`{"spec":{"strategy":%s,"template":{"metadata":{"annotations":{%q:%q}},"spec":{"containers":[{"name":%q,"env":[{"name":%q,"value":%q}]}]}}}}`,
		strategyJSON,
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

// ExecutorEnvEnabled reports whether the named environment variable on the
// executor container is set to "true". Use for feature gates that the
// Helm chart controls via executor env vars (e.g.
// EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED, ENABLE_FUNCTION_SERVICES).
func (f *Framework) ExecutorEnvEnabled(t *testing.T, ctx context.Context, name string) bool {
	t.Helper()
	dep, err := f.kubeClient.AppsV1().Deployments(f.FissionNamespace()).Get(ctx, executorDeploymentName, metav1.GetOptions{})
	require.NoErrorf(t, err, "ExecutorEnvEnabled: get executor Deployment %s/%s", f.FissionNamespace(), executorDeploymentName)
	v, _ := executorEnvValue(dep, name)
	return v == "true"
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
	f.WaitForDeploymentRollout(t, ctx, executorDeploymentName, atLeast, timeout)
}
