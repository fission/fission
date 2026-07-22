// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fakeversioned "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestFunctionVersionTypesRegistered pins scheme registration and the
// generated typed clients for the RFC-0025 CRDs: a missing addKnownTypes
// entry or a stale codegen run fails here, not at controller runtime.
func TestFunctionVersionTypesRegistered(t *testing.T) {
	t.Parallel()
	c := fakeversioned.NewSimpleClientset()

	_, err := c.CoreV1().FunctionVersions("default").Create(t.Context(), &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-v1"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:       "fn",
			FunctionUID:        types.UID("fn-uid"),
			FunctionGeneration: 1,
			Sequence:           1,
			Snapshot:           fv1.FunctionSpec{},
			PackageDigest:      "sha256:" + sample64,
			PublishedAt:        metav1.Now(),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = c.CoreV1().FunctionAliases("default").Create(t.Context(), &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-live"},
		Spec: fv1.FunctionAliasSpec{
			FunctionName: "fn",
			Version:      "fn-v1",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

// sample64 is a syntactically valid 64-hex-char digest suffix reused across
// test cases that need `sha256:<64 hex>`.
const sample64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"

// TestFunctionVersionDeepCopy proves FunctionVersion/FunctionAlias (and their
// List types) round-trip through DeepCopy with independent backing storage —
// the deepcopy-gen output that make codegen must produce for these types.
func TestFunctionVersionDeepCopy(t *testing.T) {
	t.Parallel()

	retain := 5
	weight := 50
	fv := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-v1"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:       "fn",
			FunctionUID:        types.UID("fn-uid"),
			FunctionGeneration: 1,
			Sequence:           1,
			Snapshot: fv1.FunctionSpec{
				Versioning: &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto, Retain: &retain},
			},
			PackageDigest: "sha256:" + sample64,
			PublishedAt:   metav1.Now(),
		},
	}
	dup := fv.DeepCopy()
	require.EqualValues(t, fv, dup)
	*dup.Spec.Snapshot.Versioning.Retain = 9
	require.NotEqual(t, *fv.Spec.Snapshot.Versioning.Retain, *dup.Spec.Snapshot.Versioning.Retain,
		"DeepCopy must not share backing storage for pointer fields")

	fvList := &fv1.FunctionVersionList{Items: []fv1.FunctionVersion{*fv}}
	dupList := fvList.DeepCopy()
	require.EqualValues(t, fvList, dupList)

	fa := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-live"},
		Spec: fv1.FunctionAliasSpec{
			FunctionName:     "fn",
			Version:          "fn-v1",
			Weight:           &weight,
			SecondaryVersion: "fn-v2",
		},
		Status: fv1.FunctionAliasStatus{
			ResolvedVersion: "fn-v1",
			History: []fv1.AliasTargetRecord{
				{Version: "fn-v0", PackageDigest: "sha256:" + sample64, SwitchedAt: metav1.Now()},
			},
			Conditions: []metav1.Condition{
				{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved},
			},
		},
	}
	dupAlias := fa.DeepCopy()
	require.EqualValues(t, fa, dupAlias)
	*dupAlias.Spec.Weight = 75
	require.NotEqual(t, *fa.Spec.Weight, *dupAlias.Spec.Weight,
		"DeepCopy must not share backing storage for pointer fields")
	dupAlias.Status.History[0].Version = "mutated"
	require.NotEqual(t, fa.Status.History[0].Version, dupAlias.Status.History[0].Version,
		"DeepCopy must not share backing storage for slice elements")

	faList := &fv1.FunctionAliasList{Items: []fv1.FunctionAlias{*fa}}
	dupFaList := faList.DeepCopy()
	require.EqualValues(t, faList, dupFaList)
}

// TestFunctionAliasGetConditions proves FunctionAlias satisfies the same
// conditions-accessor interface used by pkg/conditions for every other
// status-carrying CRD.
func TestFunctionAliasGetConditions(t *testing.T) {
	t.Parallel()

	conditionsGetter := interface {
		GetConditions() *[]metav1.Condition
	}(nil)
	fa := &fv1.FunctionAlias{}
	conditionsGetter = fa
	require.Same(t, &fa.Status.Conditions, conditionsGetter.GetConditions())
}
