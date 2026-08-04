// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	hpautils "github.com/fission/fission/pkg/executor/util/hpa"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func newSpecTestContainer(t *testing.T) *Container {
	t.Helper()
	logger := loggerfactory.GetLogger()
	kubeClient := fake.NewClientset()
	return &Container{
		logger:           logger,
		kubernetesClient: kubeClient,
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		nsResolver:       utils.DefaultNSResolver(),
		hpaops:           hpautils.NewHpaOperations(logger, kubeClient, "test-instance"),
	}
}

func countDeploymentUpdates(t *testing.T, caaf *Container) int {
	t.Helper()
	n := 0
	for _, a := range caaf.kubernetesClient.(*fake.Clientset).Actions() {
		if a.GetVerb() == "update" && a.GetResource().Resource == "deployments" {
			n++
		}
	}
	return n
}

// seedFnWithDeployment creates the function's deployment via the production
// spec builder (so the selector and annotations are the real ones), records it
// in the fake clientset, and registers the live fsvc entry the helper resolves
// through. The deployment's RV annotation is whatever fn.ResourceVersion was at
// seed time.
func seedFnWithDeployment(t *testing.T, caaf *Container, fn *fv1.Function) string {
	t.Helper()
	objName := caaf.getObjName(fn)
	one := int32(1)
	depl, err := caaf.getDeploymentSpec(t.Context(), fn, &one, objName, "default",
		caaf.getDeployLabels(fn.ObjectMeta), caaf.getDeployAnnotations(fn.ObjectMeta))
	require.NoError(t, err)
	_, err = caaf.kubernetesClient.AppsV1().Deployments("default").Create(t.Context(), depl, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = caaf.fsCache.Add(fscache.FuncSvc{
		Name:     objName,
		Function: &fn.ObjectMeta,
		Address:  "10.0.0.1:8888",
		Executor: fv1.ExecutorTypeContainer,
	})
	require.NoError(t, err)
	return objName
}

// TestContainerReconcileDeploymentSpec pins the helper's four branches — the
// RV-equal no-op is the idempotency claim every create-routed reconcile leans
// on, and the stale branch is the level-trigger that un-swallows a coalesced
// update.
func TestContainerReconcileDeploymentSpec(t *testing.T) {
	t.Parallel()

	t.Run("no live cache entry is a no-op", func(t *testing.T) {
		t.Parallel()
		caaf := newSpecTestContainer(t)
		fn := newTestContainerFunction()
		fn.UID = "00000000-0000-0000-0000-000000000001"
		require.NoError(t, caaf.reconcileDeploymentSpec(t.Context(), fn))
		assert.Zero(t, countDeploymentUpdates(t, caaf))
	})

	t.Run("deployment gone is a no-op, not an error", func(t *testing.T) {
		t.Parallel()
		caaf := newSpecTestContainer(t)
		fn := newTestContainerFunction()
		fn.UID = "00000000-0000-0000-0000-000000000002"
		_, err := caaf.fsCache.Add(fscache.FuncSvc{
			Name: "vanished", Function: &fn.ObjectMeta, Address: "10.0.0.9:8888", Executor: fv1.ExecutorTypeContainer,
		})
		require.NoError(t, err)
		require.NoError(t, caaf.reconcileDeploymentSpec(t.Context(), fn))
		assert.Zero(t, countDeploymentUpdates(t, caaf))
	})

	t.Run("matching RV annotation issues no update", func(t *testing.T) {
		t.Parallel()
		caaf := newSpecTestContainer(t)
		fn := newTestContainerFunction()
		fn.UID = "00000000-0000-0000-0000-000000000003"
		fn.ResourceVersion = "41"
		seedFnWithDeployment(t, caaf, fn)
		require.NoError(t, caaf.reconcileDeploymentSpec(t.Context(), fn))
		assert.Zero(t, countDeploymentUpdates(t, caaf), "an already-current deployment must not be rewritten")
	})

	t.Run("stale RV annotation pushes the current spec", func(t *testing.T) {
		t.Parallel()
		caaf := newSpecTestContainer(t)
		fn := newTestContainerFunction()
		fn.UID = "00000000-0000-0000-0000-000000000004"
		fn.ResourceVersion = "41"
		objName := seedFnWithDeployment(t, caaf, fn)

		fn.ResourceVersion = "42" // the update the deployment never saw
		require.NoError(t, caaf.reconcileDeploymentSpec(t.Context(), fn))
		assert.Equal(t, 1, countDeploymentUpdates(t, caaf), "a stale deployment must be converged")

		depl, err := caaf.kubernetesClient.AppsV1().Deployments("default").Get(t.Context(), objName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "42", depl.Annotations[fv1.FUNCTION_RESOURCE_VERSION],
			"the converged deployment must carry the new function RV")
	})
}

// TestContainerUpdateFuncDeploymentSelectorGuard pins the immutable-selector
// guard: a Function whose CR labels changed (which changes the computed
// selector without bumping Generation) must be left in place with a nil
// return — an Update would be rejected by the API server, and an error here
// requeues forever against a permanently immutable field.
func TestContainerUpdateFuncDeploymentSelectorGuard(t *testing.T) {
	t.Parallel()
	caaf := newSpecTestContainer(t)
	fn := newTestContainerFunction()
	fn.UID = "00000000-0000-0000-0000-000000000005"
	fn.ResourceVersion = "41"
	seedFnWithDeployment(t, caaf, fn)

	fn.Labels = map[string]string{"team": "other"} // changes getDeployLabels -> selector
	fn.ResourceVersion = "42"
	require.NoError(t, caaf.updateFuncDeployment(t.Context(), fn),
		"a selector change must be skipped, never surfaced as a requeue-forever error")
	assert.Zero(t, countDeploymentUpdates(t, caaf), "no in-place Update may be attempted across a selector change")
}
