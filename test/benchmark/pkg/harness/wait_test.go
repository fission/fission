// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/test/benchmark/pkg/cluster"
)

// TestWaitForProvisionedFloor pins that the poll queries the function in the
// caller's namespace (regression guard: an earlier version used
// metav1.NamespaceAll, which silently never matched the function the
// scenario actually created) and correctly gates on Status.ProvisionedReady.
func TestWaitForProvisionedFloor(t *testing.T) {
	t.Parallel()

	t.Run("reaches floor", func(t *testing.T) {
		t.Parallel()
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "bench"},
			Status:     fv1.FunctionStatus{ProvisionedReady: 3},
		}
		e := &Env{Clients: &cluster.Clients{Fission: fake.NewSimpleClientset(fn)}, Namespace: "bench"}

		err := e.WaitForProvisionedFloor(t.Context(), "fn", 3, 5*time.Second)
		require.NoError(t, err)
	})

	t.Run("never reaches floor times out", func(t *testing.T) {
		t.Parallel()
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "bench"},
			Status:     fv1.FunctionStatus{ProvisionedReady: 1},
		}
		e := &Env{Clients: &cluster.Clients{Fission: fake.NewSimpleClientset(fn)}, Namespace: "bench"}

		err := e.WaitForProvisionedFloor(t.Context(), "fn", 3, 0)
		assert.ErrorContains(t, err, "timed out")
	})

	t.Run("wrong namespace never matches", func(t *testing.T) {
		t.Parallel()
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "bench"},
			Status:     fv1.FunctionStatus{ProvisionedReady: 3},
		}
		e := &Env{Clients: &cluster.Clients{Fission: fake.NewSimpleClientset(fn)}, Namespace: "other"}

		err := e.WaitForProvisionedFloor(t.Context(), "fn", 3, 0)
		require.Error(t, err)
	})
}
