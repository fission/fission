// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/router/routetable"
)

// incrVersion builds a FunctionVersion CR pinning (uid, gen) of fnName.
func incrVersion(name, ns, fnName string, uid types.UID, gen, seq int64) *fv1.FunctionVersion {
	return &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:       fnName,
			FunctionUID:        uid,
			FunctionGeneration: gen,
			Sequence:           seq,
			Snapshot:           fv1.FunctionSpec{},
			PackageDigest:      "sha256:3333333333333333333333333333333333333333333333333333333333333",
			PublishedAt:        metav1.Now(),
		},
	}
}

// incrVersionedTrigger builds an HTTPTrigger pinned to a FunctionVersion (an
// RFC-0025 Version reference) rather than the live Function.
func incrVersionedTrigger(name, ns string, gen int64, url, fnName, version string) *fv1.HTTPTrigger {
	return &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: gen, UID: types.UID("trig-" + name)},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: url,
			Methods:     []string{http.MethodGet},
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName, Name: fnName, Version: version,
			},
		},
	}
}

// TestIncrementalVersionedTriggerReResolvesOnFunctionEvent is the
// plan-review regression (warning #4): a versioned trigger's FnGens key is a
// BackendKey ("hello@hello-v1"), not the live function's plain name. Without
// reindexLocked stripping the "@version" suffix back to the plain name
// before populating fnIndex, a live-Function DELETE event would never find
// this trigger via TriggersForFunction (which the cascade always queries by
// plain name) — so the trigger's route would incorrectly keep serving after
// its underlying function is gone. With the fix, the delete cascades: the
// route drops and the trigger is marked FunctionNotFound.
func TestIncrementalVersionedTriggerReResolvesOnFunctionEvent(t *testing.T) {
	fn := incrFn("hello", "myns", 1) // UID: "fn-hello" (incrFn's convention)
	v := incrVersion("hello-v1", "myns", "hello", fn.UID, fn.Generation, 1)
	trigger := incrVersionedTrigger("t1", "myns", 1, "/hello", "hello", "hello-v1")
	ts, cl := newIncrementalTS(t, fn, v, trigger)
	ts.fissionClient = fissionfake.NewSimpleClientset(trigger)

	// Seed: function insert + versioned-trigger insert, both resolve.
	_, err := ts.applyFunctionIncremental(t.Context(), fn)
	require.NoError(t, err)
	res, err := ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	require.Equal(t, routetable.ShapeChanged, res, "the versioned trigger must resolve and admit on first apply")
	requireSignal(t, ts)
	ts.materialize(t.Context())

	public, _ := muxes(ts)
	require.True(t, muxMatches(public, http.MethodGet, "/hello"), "versioned route must serve once resolved")

	// Live-Function DELETE event: this is the cascade under test. It must
	// find the versioned trigger via TriggersForFunction({myns, "hello"})
	// (the reindexLocked fix) and re-apply it, which now fails to resolve
	// (the live Function backing the pinned version is gone) and drops the
	// route.
	require.NoError(t, cl.Delete(t.Context(), fn))
	err = ts.deleteFunctionIncremental(t.Context(), types.NamespacedName{Namespace: "myns", Name: "hello"})
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	public, _ = muxes(ts)
	assert.False(t, muxMatches(public, http.MethodGet, "/hello"),
		"versioned trigger's route must drop once its live function is deleted (fnIndex cascade)")

	got, err := ts.fissionClient.CoreV1().HTTPTriggers("myns").Get(t.Context(), "t1", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, got, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionFalse, fv1.HTTPTriggerReasonFunctionNotFound)
}

// TestIncrementalVersionedTriggerFnGensUsesBackendKey pins that a versioned
// trigger's applied RouteSpec.FnGens is actually keyed by BackendKey (not
// the plain function name) — the precondition the reindex-strip regression
// above depends on. A same-shape re-apply with an unchanged version pin must
// be NoChange (FnGens equality holds across identical BackendKeys).
func TestIncrementalVersionedTriggerFnGensUsesBackendKey(t *testing.T) {
	fn := incrFn("hello", "myns", 1)
	v := incrVersion("hello-v1", "myns", "hello", fn.UID, fn.Generation, 1)
	trigger := incrVersionedTrigger("t1", "myns", 1, "/hello", "hello", "hello-v1")
	ts, _ := newIncrementalTS(t, fn, v, trigger)

	res, err := ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	require.Equal(t, routetable.ShapeChanged, res)

	// Re-apply the identical trigger: FnGens must compare equal (same
	// BackendKey, same Generation) for a NoChange result.
	res, err = ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	assert.Equal(t, routetable.NoChange, res, "identical versioned re-apply must be a no-op")
}
