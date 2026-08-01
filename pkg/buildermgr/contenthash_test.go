// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestPackageContentHash(t *testing.T) {
	t.Parallel()

	oci := func(image, digest string) fv1.PackageSpec {
		return fv1.PackageSpec{Deployment: fv1.Archive{
			Type: fv1.ArchiveTypeOCI,
			OCI:  &fv1.OCIArchive{Image: image, Digest: digest},
		}}
	}
	url := func(u, sum string) fv1.PackageSpec {
		return fv1.PackageSpec{Deployment: fv1.Archive{
			Type:     fv1.ArchiveTypeUrl,
			URL:      u,
			Checksum: fv1.Checksum{Type: fv1.ChecksumTypeSHA256, Sum: sum},
		}}
	}

	t.Run("deterministic and non-empty", func(t *testing.T) {
		t.Parallel()
		s := oci("registry/app:v1", "sha256:"+repeat("a", 64))
		h := PackageContentHash(s)
		assert.NotEmpty(t, h)
		assert.Equal(t, h, PackageContentHash(s), "same spec must hash the same")
		// The empty package still hashes, so "no content" stays
		// distinguishable from "not yet recorded" (the empty string).
		assert.NotEmpty(t, PackageContentHash(fv1.PackageSpec{}))
	})

	t.Run("changes when delivered content changes", func(t *testing.T) {
		t.Parallel()
		base := oci("registry/app:v1", "sha256:"+repeat("a", 64))
		cases := map[string]fv1.PackageSpec{
			"digest":   oci("registry/app:v1", "sha256:"+repeat("b", 64)),
			"image":    oci("registry/app:v2", "sha256:"+repeat("a", 64)),
			"url":      url("http://storage/a", "abc"),
			"checksum": url("http://storage/a", "def"),
			"literal": {Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeLiteral, Literal: []byte("code")}},
			"source added": {
				Source:     fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "http://storage/src"},
				Deployment: base.Deployment,
			},
		}
		seen := map[string]string{"base": PackageContentHash(base)}
		for name, spec := range cases {
			h := PackageContentHash(spec)
			for prev, prevHash := range seen {
				assert.NotEqualf(t, prevHash, h, "%s must not collide with %s", name, prev)
			}
			seen[name] = h
		}
	})

	t.Run("stable across delivery-only metadata", func(t *testing.T) {
		t.Parallel()
		// Rotating a pull secret or pointing at a different subdirectory
		// changes how the same bytes are reached, not what they are — hashing
		// them would rebuild the world on a credential rotation.
		base := oci("registry/app:v1", "sha256:"+repeat("a", 64))
		withExtras := oci("registry/app:v1", "sha256:"+repeat("a", 64))
		withExtras.Deployment.OCI.ImagePullSecrets = []apiv1.LocalObjectReference{{Name: "regcred"}}
		withExtras.Deployment.OCI.SubPath = "app"
		assert.Equal(t, PackageContentHash(base), PackageContentHash(withExtras))
	})

	t.Run("length-prefixed so fields cannot be spliced", func(t *testing.T) {
		t.Parallel()
		// Without length prefixing, image "a"+digest "bc" would hash the same
		// as image "ab"+digest "c", and two different packages would look
		// unchanged.
		a := oci("a", "bc")
		b := oci("ab", "c")
		require.NotEqual(t, PackageContentHash(a), PackageContentHash(b))
	})
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n)
	for range n {
		out = append(out, s[0])
	}
	return string(out)
}

// TestBuildTriggerPredicateFiresOnSpecChange pins the wiring the content-change
// path depends on. The predicate originally enqueued ONLY on a BuildStatus
// transition into pending, which is what the CLI's poke produces — so a
// Git-applied archive or OCI digest change, which moves the spec and not the
// status, enqueued nothing and left the whole feature unreachable.
func TestBuildTriggerPredicateFiresOnSpecChange(t *testing.T) {
	t.Parallel()

	p := buildTriggerPredicate()
	pkg := func(gen int64, status fv1.BuildStatus) *fv1.Package {
		return &fv1.Package{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default", Generation: gen},
			Status:     fv1.PackageStatus{BuildStatus: status},
		}
	}

	cases := []struct {
		name     string
		old, new *fv1.Package
		want     bool
	}{
		{"spec changed (the GitOps case)", pkg(1, fv1.BuildStatusNone), pkg(2, fv1.BuildStatusNone), true},
		{"status poked into pending (the CLI case)", pkg(1, fv1.BuildStatusNone), pkg(1, fv1.BuildStatusPending), true},
		{"nothing changed", pkg(1, fv1.BuildStatusSucceeded), pkg(1, fv1.BuildStatusSucceeded), false},
		// The reconciler's own status writes must not re-trigger it, or it
		// would loop on itself.
		{"reconciler's own running write", pkg(1, fv1.BuildStatusPending), pkg(1, fv1.BuildStatusRunning), false},
		{"reconciler's own succeeded write", pkg(1, fv1.BuildStatusRunning), pkg(1, fv1.BuildStatusSucceeded), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := p.Update(event.UpdateEvent{ObjectOld: tc.old, ObjectNew: tc.new})
			assert.Equal(t, tc.want, got)
		})
	}
}
