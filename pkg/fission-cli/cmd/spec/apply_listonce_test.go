// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestSpecApplyListsPackagesOnce pins the fix for the double cluster-wide
// Package enumeration: applyArchives (archive dedup map) and applyPackages
// (desired-vs-live diff) both need the full Package list, and before the fix
// each issued its own List, so one `spec apply` listed Packages twice. The list
// is now fetched once in applyResources and threaded to both, so a single apply
// must issue exactly one Packages.List — asserted here via a fake-clientset
// reactor counting the "list" verb on "packages".
func TestSpecApplyListsPackagesOnce(t *testing.T) {
	t.Parallel()

	// A couple of cluster packages so both consumers have real data to walk.
	existing := []runtime.Object{
		&fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: "pkg-a", Namespace: "default"}},
		&fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: "pkg-b", Namespace: "other"}},
	}
	//nolint:staticcheck // SMD schema not yet generated for NewClientset, see k8s#126850
	fc := fissionfake.NewSimpleClientset(existing...)

	var packageLists int
	// PrependReactor runs before the default tracker; returning handled=false
	// lets the real List proceed, so we only count without intercepting.
	fc.PrependReactor("list", "packages", func(action k8stesting.Action) (bool, runtime.Object, error) {
		packageLists++
		return false, nil, nil
	})

	fclient := cmd.Client{
		FissionClientSet: fc,
		KubernetesClient: k8sfake.NewSimpleClientset(),
	}

	// An empty spec with dry-run makes the apply read-only: it still runs the
	// full list -> diff for every kind (so both package consumers execute) but
	// performs no create/update/delete against the cluster.
	input := fakeInput{ctx: t.Context()}
	fr := &FissionResources{}

	_, _, err := applyResources(input, fclient, "" /*specDir*/, fr,
		false /*delete*/, false /*specAllowConflicts*/, true /*dryRun*/)
	require.NoError(t, err)

	assert.Equal(t, 1, packageLists,
		"a single spec apply must list cluster Packages exactly once (was twice before the shared-snapshot fix)")
}
