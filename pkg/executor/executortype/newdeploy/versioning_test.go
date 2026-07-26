// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// fnForObjName builds a Function with a 36-char UUID-shaped UID (getObjName
// slices the last 17 chars of fn.UID, matching every real Kubernetes UID).
func fnForObjName(name, namespace string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "83c82da2-81e9-4ebd-867e-f383e65e603f",
		},
	}
}

// TestGetObjName is a thin delegation smoke test: getObjName reaches
// executorUtils.VersionedObjName with the "newdeploy-" prefix and a
// versioned Function still yields a distinct, bounded name. The full
// length-bound property tests and hash-fallback table live once, against
// VersionedObjName itself, in pkg/executor/util/version_test.go.
func TestGetObjName(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}

	fn := fnForObjName("hello", "default")
	unversioned := deploy.getObjName(fn)
	assert.Equal(t, unversioned, deploy.getObjName(fn), "name must be stable")
	assert.Contains(t, unversioned, "newdeploy-")
	assert.LessOrEqual(t, len(unversioned), 63)

	fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
	versioned := deploy.getObjName(fn)
	assert.LessOrEqual(t, len(versioned), 63)
	assert.NotEqual(t, unversioned, versioned, "a versioned name must not collide with the unversioned one")
	assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
}

// TestGetDeployLabelsPropagatesFunctionVersion asserts FUNCTION_VERSION
// flows from the versioned Function object's labels into the Deployment
// labels via getDeployLabels' existing fnMeta.Labels merge — no new
// merge logic needed, just coverage that the merge actually carries the
// label through.
func TestGetDeployLabelsPropagatesFunctionVersion(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	fnMeta := metav1.ObjectMeta{
		Name:      "hello",
		Namespace: "default",
		UID:       "fn-uid",
		Labels:    map[string]string{fv1.FUNCTION_VERSION: "hello-v3"},
	}
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	labels := deploy.getDeployLabels(fnMeta, envMeta)

	assert.Equal(t, "hello-v3", labels[fv1.FUNCTION_VERSION], "FUNCTION_VERSION must propagate to Deployment labels")
}

// TestGetDeployLabelsUnversionedHasNoVersionLabel is the byte-identical
// control: an unversioned function's labels must not carry FUNCTION_VERSION.
func TestGetDeployLabelsUnversionedHasNoVersionLabel(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	fnMeta := metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: "fn-uid"}
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	labels := deploy.getDeployLabels(fnMeta, envMeta)

	_, has := labels[fv1.FUNCTION_VERSION]
	assert.False(t, has, "unversioned function must not carry FUNCTION_VERSION label")
}

// TestGetFuncSvcFromCacheVersionPinned is the C1 regression at the manager
// boundary: the executor caches distinct fsvcs for a live function (gen 5)
// and a versioned projection (gen 2) sharing one UID, each backing its own
// Deployment. GetFuncSvcFromCache for the version-pinned projection must
// return the projection's own service — never the live one that a bare-UID
// (last-writer-wins) index would have handed back, silently executing the
// wrong version's code. It then locks the C8 half: evicting the versioned
// entry (IsValid-failure path, DeleteFuncSvcFromCache) must leave the live
// entry reachable through the UID index.
func TestGetFuncSvcFromCacheVersionPinned(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{fsCache: fscache.MakeFunctionServiceCache(loggerfactory.GetLogger())}
	ctx := t.Context()

	liveFn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hello", Namespace: "default",
			UID: "83c82da2-81e9-4ebd-867e-f383e65e603f", Generation: 5,
		},
	}
	// Versioned projection: same UID, pinned older Generation, version label
	// (the shape versioning.VersionedFunction produces).
	v1Fn := liveFn.DeepCopy()
	v1Fn.Generation = 2
	v1Fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}

	liveSvc := fscache.FuncSvc{
		Name:     deploy.getObjName(liveFn),
		Function: &liveFn.ObjectMeta,
		Address:  "10.9.0.5:8888",
		Executor: fv1.ExecutorTypeNewdeploy,
	}
	v1Svc := fscache.FuncSvc{
		Name:     deploy.getObjName(v1Fn),
		Function: &v1Fn.ObjectMeta,
		Address:  "10.9.0.2:8888",
		Executor: fv1.ExecutorTypeNewdeploy,
	}
	_, err := deploy.fsCache.Add(liveSvc)
	require.NoError(t, err)
	// Added last: a bare-UID index would leave the versioned meta as the
	// UID's only entry.
	_, err = deploy.fsCache.Add(v1Svc)
	require.NoError(t, err)

	got, err := deploy.GetFuncSvcFromCache(ctx, v1Fn)
	require.NoError(t, err, "version-pinned request must hit its own generation's cache entry")
	assert.Equal(t, v1Svc.Address, got.Address, "gen-2 request must never be routed to the live gen-5 Deployment")
	assert.Equal(t, v1Svc.Name, got.Name)

	got, err = deploy.GetFuncSvcFromCache(ctx, liveFn)
	require.NoError(t, err)
	assert.Equal(t, liveSvc.Address, got.Address, "live request must never be routed to the versioned Deployment")

	// C8: evicting one version's entry must not orphan the survivor's UID
	// index entry (ListByFunctionUID feeds fnDelete; ListOld feeds the idle
	// reaper).
	deploy.DeleteFuncSvcFromCache(ctx, &v1Svc)

	got, err = deploy.GetFuncSvcFromCache(ctx, liveFn)
	require.NoError(t, err, "evicting the versioned entry must leave the live entry reachable")
	assert.Equal(t, liveSvc.Address, got.Address)

	remaining := deploy.fsCache.ListByFunctionUID(liveFn.UID)
	require.Len(t, remaining, 1)
	assert.Equal(t, liveSvc.Address, remaining[0].Address)
}
