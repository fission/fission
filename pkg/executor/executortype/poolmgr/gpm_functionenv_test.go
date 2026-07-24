// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/cache"
	"github.com/fission/fission/pkg/crd"
)

// TestGetFunctionEnvStatusOnlyResourceVersionBump locks the #3596
// status-churn fix for the poolmgr's functionEnv cache: a Function
// status-only update bumps ResourceVersion but not Generation, and must
// not miss the cache entry populated for an earlier RV of the same spec
// (which would force an unnecessary re-fetch of the Environment via
// crClient on every status update).
func TestGetFunctionEnvStatusOnlyResourceVersionBump(t *testing.T) {
	t.Parallel()

	env := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env1", Namespace: "default"},
	}
	crClient := crfake.NewClientBuilder().WithScheme(scheme()).WithObjects(env).Build()

	gpm := &GenericPoolManager{
		logger:      logr.Discard(),
		crClient:    crClient,
		functionEnv: cache.MakeCache[crd.CacheKeyUG, *fv1.Environment](10*time.Second, 0),
	}

	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "fn1",
			Namespace:       "default",
			UID:             "fn-uid-1",
			Generation:      1,
			ResourceVersion: "100",
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "env1", Namespace: "default"},
		},
	}

	ctx := t.Context()
	got, err := gpm.getFunctionEnv(ctx, fn)
	require.NoError(t, err)
	require.Equal(t, "env1", got.Name)

	// Simulate a status-only update observed by a later caller: same
	// UID+Generation, bumped ResourceVersion.
	fnAfterStatusUpdate := fn.DeepCopy()
	fnAfterStatusUpdate.ResourceVersion = "200"

	// Remove the Environment from the crClient so a cache miss would be
	// detectable (Get would fail / return a not-found error instead of
	// silently succeeding a second time).
	require.NoError(t, crClient.Delete(ctx, env))

	got2, err := gpm.getFunctionEnv(ctx, fnAfterStatusUpdate)
	require.NoError(t, err, "status-only RV bump must hit the cache, not re-fetch from crClient")
	require.Equal(t, "env1", got2.Name)
}
