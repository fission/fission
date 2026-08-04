// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// TestNewdeployReconcileDeploymentSpec pins the helper's cheap branches (the
// full stale-push path needs an Environment and is exercised by the container
// twin, whose helper mirrors this one, and by the integration suite). The
// RV-equal no-op is the idempotency claim every create-routed reconcile leans
// on.
func TestNewdeployReconcileDeploymentSpec(t *testing.T) {
	t.Parallel()

	newDeployFixture := func() (*NewDeploy, *fake.Clientset) {
		logger := loggerfactory.GetLogger()
		kubeClient := fake.NewClientset()
		return &NewDeploy{
			logger:           logger,
			kubernetesClient: kubeClient,
			fsCache:          fscache.MakeFunctionServiceCache(logger),
			nsResolver:       utils.DefaultNSResolver(),
		}, kubeClient
	}
	countUpdates := func(client *fake.Clientset) int {
		n := 0
		for _, a := range client.Actions() {
			if a.GetVerb() == "update" && a.GetResource().Resource == "deployments" {
				n++
			}
		}
		return n
	}

	t.Run("no live cache entry is a no-op", func(t *testing.T) {
		t.Parallel()
		deploy, client := newDeployFixture()
		fn := fnOfType("fn", fv1.ExecutorTypeNewdeploy)
		fn.UID = "00000000-0000-0000-0000-0000000000a1"
		require.NoError(t, deploy.reconcileDeploymentSpec(t.Context(), fn))
		assert.Zero(t, countUpdates(client))
	})

	t.Run("deployment gone is a no-op, not an error", func(t *testing.T) {
		t.Parallel()
		deploy, client := newDeployFixture()
		fn := fnOfType("fn", fv1.ExecutorTypeNewdeploy)
		fn.UID = "00000000-0000-0000-0000-0000000000a2"
		_, err := deploy.fsCache.Add(fscache.FuncSvc{
			Name: "vanished", Function: &fn.ObjectMeta, Address: "10.0.0.9:8888", Executor: fv1.ExecutorTypeNewdeploy,
		})
		require.NoError(t, err)
		require.NoError(t, deploy.reconcileDeploymentSpec(t.Context(), fn))
		assert.Zero(t, countUpdates(client))
	})

	t.Run("matching RV annotation issues no update", func(t *testing.T) {
		t.Parallel()
		deploy, client := newDeployFixture()
		fn := fnOfType("fn", fv1.ExecutorTypeNewdeploy)
		fn.UID = "00000000-0000-0000-0000-0000000000a3"
		fn.ResourceVersion = "41"
		_, err := client.AppsV1().Deployments("default").Create(t.Context(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nd-fn", Namespace: "default",
				Annotations: map[string]string{fv1.FUNCTION_RESOURCE_VERSION: "41"},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = deploy.fsCache.Add(fscache.FuncSvc{
			Name: "nd-fn", Function: &fn.ObjectMeta, Address: "10.0.0.1:8888", Executor: fv1.ExecutorTypeNewdeploy,
		})
		require.NoError(t, err)
		require.NoError(t, deploy.reconcileDeploymentSpec(t.Context(), fn))
		assert.Zero(t, countUpdates(client), "an already-current deployment must not be rewritten")
	})
}
