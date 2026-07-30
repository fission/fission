// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versionretain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func version(ns, name string, uid types.UID, gen int64) fv1.FunctionVersion {
	return fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: fv1.FunctionVersionSpec{
			FunctionUID:        uid,
			FunctionGeneration: gen,
		},
	}
}

func TestView_EmptyUntilRebuild(t *testing.T) {
	v := New()
	assert.False(t, v.Retained("fn-uid", 1), "nothing retained before the first Rebuild")
}

func TestView_Rebuild_VersionRetainedBySpecVersion(t *testing.T) {
	v := New()
	aliases := []fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "prod"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		},
	}
	versions := []fv1.FunctionVersion{
		version("ns1", "fn-v1", "fn-uid", 3),
	}
	v.Rebuild(aliases, versions)

	assert.True(t, v.Retained("fn-uid", 3), "alias-referenced generation is retained")
	assert.False(t, v.Retained("fn-uid", 4), "a different generation is not retained")
	assert.False(t, v.Retained("other-uid", 3), "a different function UID is not retained")
}

func TestView_Rebuild_SecondaryVersionRetained(t *testing.T) {
	v := New()
	weight := 50
	aliases := []fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "canary"},
			Spec: fv1.FunctionAliasSpec{
				FunctionName:     "fn",
				Version:          "fn-v2",
				Weight:           &weight,
				SecondaryVersion: "fn-v1",
			},
		},
	}
	versions := []fv1.FunctionVersion{
		version("ns1", "fn-v1", "fn-uid", 1),
		version("ns1", "fn-v2", "fn-uid", 2),
	}
	v.Rebuild(aliases, versions)

	assert.True(t, v.Retained("fn-uid", 1), "secondaryVersion (canary weight target) is retained")
	assert.True(t, v.Retained("fn-uid", 2), "primary version is retained")
}

func TestView_Rebuild_DigestAliasUsesResolvedVersion(t *testing.T) {
	v := New()
	aliases := []fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "prod"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", PackageDigest: "sha256:deadbeef"},
			Status:     fv1.FunctionAliasStatus{ResolvedVersion: "fn-v7"},
		},
	}
	versions := []fv1.FunctionVersion{
		version("ns1", "fn-v7", "fn-uid", 7),
	}
	v.Rebuild(aliases, versions)

	assert.True(t, v.Retained("fn-uid", 7), "digest-pinned alias retains status.resolvedVersion's generation")
}

func TestView_Rebuild_MissingVersionRetainsNothing(t *testing.T) {
	v := New()
	aliases := []fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "prod"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v-missing"},
		},
	}
	// No matching FunctionVersion for fn-v-missing.
	v.Rebuild(aliases, nil)

	assert.False(t, v.Retained("fn-uid", 1))
}

func TestView_Rebuild_NamespaceScoped(t *testing.T) {
	v := New()
	aliases := []fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "prod"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		},
	}
	// Same version name, different namespace — must not resolve.
	versions := []fv1.FunctionVersion{
		version("ns2", "fn-v1", "fn-uid", 1),
	}
	v.Rebuild(aliases, versions)

	assert.False(t, v.Retained("fn-uid", 1), "a version in a different namespace must not resolve")
}

// TestView_Rebuild_AliasRepointChangesTheSet locks the "warm rollback"
// scenario the RFC targets: moving an alias off an older generation and back
// updates the retained set both ways, without carrying stale entries between
// Rebuild calls (the full-recompute contract — no leftover retain from a
// previous snapshot).
func TestView_Rebuild_AliasRepointChangesTheSet(t *testing.T) {
	v := New()
	versions := []fv1.FunctionVersion{
		version("ns1", "fn-v1", "fn-uid", 1),
		version("ns1", "fn-v2", "fn-uid", 2),
	}

	// Alias initially points at v2 (latest).
	v.Rebuild([]fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "prod"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v2"},
		},
	}, versions)
	assert.True(t, v.Retained("fn-uid", 2))
	assert.False(t, v.Retained("fn-uid", 1))

	// Rollback: alias repointed at v1 (an older generation).
	v.Rebuild([]fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "prod"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		},
	}, versions)
	assert.True(t, v.Retained("fn-uid", 1), "rollback target is now retained")
	assert.False(t, v.Retained("fn-uid", 2), "no-longer-referenced generation stops being retained")
}

func TestView_Rebuild_EmptyReferenceNamesIgnored(t *testing.T) {
	v := New()
	aliases := []fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns1", Name: "prod"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn"}, // no Version/SecondaryVersion/ResolvedVersion
		},
	}
	v.Rebuild(aliases, []fv1.FunctionVersion{version("ns1", "", "fn-uid", 1)})
	assert.False(t, v.Retained("fn-uid", 1))
}
