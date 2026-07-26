// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

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

// TestContainerGetObjName is a thin delegation smoke test: getObjName
// reaches executorUtils.VersionedObjName with the "container-" prefix and a
// versioned Function still yields a distinct, bounded name. The full
// length-bound property tests and hash-fallback table live once, against
// VersionedObjName itself, in pkg/executor/util/version_test.go.
func TestContainerGetObjName(t *testing.T) {
	t.Parallel()
	caaf := &Container{}

	fn := fnForObjName("hello", "default")
	unversioned := caaf.getObjName(fn)
	assert.Equal(t, unversioned, caaf.getObjName(fn), "name must be stable")
	assert.Contains(t, unversioned, "container-")
	assert.LessOrEqual(t, len(unversioned), 63)

	fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
	versioned := caaf.getObjName(fn)
	assert.LessOrEqual(t, len(versioned), 63)
	assert.NotEqual(t, unversioned, versioned, "a versioned name must not collide with the unversioned one")
	assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
}

// TestContainerGetDeployLabelsPropagatesFunctionVersion asserts
// FUNCTION_VERSION flows from the versioned Function object's labels into
// the Deployment labels via getDeployLabels' existing fnMeta.Labels copy —
// no new merge logic needed, just coverage that the copy actually carries
// the label through.
func TestContainerGetDeployLabelsPropagatesFunctionVersion(t *testing.T) {
	t.Parallel()
	caaf := &Container{}
	fnMeta := metav1.ObjectMeta{
		Name:      "hello",
		Namespace: "default",
		UID:       "fn-uid",
		Labels:    map[string]string{fv1.FUNCTION_VERSION: "hello-v3"},
	}

	labels := caaf.getDeployLabels(fnMeta)

	assert.Equal(t, "hello-v3", labels[fv1.FUNCTION_VERSION], "FUNCTION_VERSION must propagate to Deployment labels")
}

// TestContainerGetDeployLabelsUnversionedHasNoVersionLabel is the
// byte-identical control: an unversioned function's labels must not carry
// FUNCTION_VERSION.
func TestContainerGetDeployLabelsUnversionedHasNoVersionLabel(t *testing.T) {
	t.Parallel()
	caaf := &Container{}
	fnMeta := metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: "fn-uid"}

	labels := caaf.getDeployLabels(fnMeta)

	_, has := labels[fv1.FUNCTION_VERSION]
	assert.False(t, has, "unversioned function must not carry FUNCTION_VERSION label")
}

// TestContainerGetFuncSvcFromCacheVersionPinned is the C1 regression at the
// container-executor boundary — see the newdeploy twin for the full story:
// a version-pinned projection's lookup must return its own generation's
// service (never the live sibling a bare-UID last-writer-wins index handed
// back), and evicting one generation's entry (C8) must leave the survivor
// reachable through the UID index.
func TestContainerGetFuncSvcFromCacheVersionPinned(t *testing.T) {
	t.Parallel()
	caaf := &Container{fsCache: fscache.MakeFunctionServiceCache(loggerfactory.GetLogger())}
	ctx := t.Context()

	liveFn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hello", Namespace: "default",
			UID: "83c82da2-81e9-4ebd-867e-f383e65e603f", Generation: 5,
		},
	}
	v1Fn := liveFn.DeepCopy()
	v1Fn.Generation = 2
	v1Fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}

	liveSvc := fscache.FuncSvc{
		Name:     caaf.getObjName(liveFn),
		Function: &liveFn.ObjectMeta,
		Address:  "10.9.1.5:8888",
		Executor: fv1.ExecutorTypeContainer,
	}
	v1Svc := fscache.FuncSvc{
		Name:     caaf.getObjName(v1Fn),
		Function: &v1Fn.ObjectMeta,
		Address:  "10.9.1.2:8888",
		Executor: fv1.ExecutorTypeContainer,
	}
	_, err := caaf.fsCache.Add(liveSvc)
	require.NoError(t, err)
	_, err = caaf.fsCache.Add(v1Svc) // added last: bare-UID index would keep only this meta
	require.NoError(t, err)

	got, err := caaf.GetFuncSvcFromCache(ctx, v1Fn)
	require.NoError(t, err, "version-pinned request must hit its own generation's cache entry")
	assert.Equal(t, v1Svc.Address, got.Address, "gen-2 request must never be routed to the live gen-5 Deployment")

	got, err = caaf.GetFuncSvcFromCache(ctx, liveFn)
	require.NoError(t, err)
	assert.Equal(t, liveSvc.Address, got.Address, "live request must never be routed to the versioned Deployment")

	caaf.DeleteFuncSvcFromCache(ctx, &v1Svc)

	got, err = caaf.GetFuncSvcFromCache(ctx, liveFn)
	require.NoError(t, err, "evicting the versioned entry must leave the live entry reachable")
	assert.Equal(t, liveSvc.Address, got.Address)

	remaining := caaf.fsCache.ListByFunctionUID(liveFn.UID)
	require.Len(t, remaining, 1)
	assert.Equal(t, liveSvc.Address, remaining[0].Address)
}
