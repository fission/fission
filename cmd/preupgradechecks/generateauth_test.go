// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const testAuthSecret = "fission-internal-auth"

func masterIn(t *testing.T, cs kubernetes.Interface, ns string) []byte {
	t.Helper()
	s, err := cs.CoreV1().Secrets(ns).Get(t.Context(), testAuthSecret, metav1.GetOptions{})
	require.NoErrorf(t, err, "expected the master in %q", ns)
	return s.Data[authSecretKey]
}

func TestGenerateAuthSecret(t *testing.T) {
	t.Parallel()
	logger := loggerfactory.GetLogger()

	// STATIC tenancy: the master is replicated because kubelet cannot resolve a
	// cross-namespace secretKeyRef, so builder and function pods in
	// defaultNamespace need a local copy. Every copy must hold the SAME value —
	// they are one HMAC master, not three.
	t.Run("static tenancy: one value across every namespace", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewClientset()
		nss := []string{"fission", "default", "fission-fn"}
		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, nss, testAuthSecret))

		want := masterIn(t, cs, "fission")
		assert.NotEmpty(t, want)
		for _, ns := range nss[1:] {
			assert.Equalf(t, want, masterIn(t, cs, ns),
				"every namespace must carry the same master; %q differs", ns)
		}
	})

	// DYNAMIC/CLUSTER tenancy: the chart passes ONLY the release namespace,
	// because tenant namespaces get controller-owned derived keys and the
	// master must never land there. This asserts the hook writes exactly what
	// it is given and invents no fan-out of its own.
	t.Run("dynamic tenancy: the master reaches only the namespace it was given", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewClientset()
		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, []string{"fission"}, testAuthSecret))

		assert.NotEmpty(t, masterIn(t, cs, "fission"))
		for _, tenant := range []string{"default", "tenant-a"} {
			_, err := cs.CoreV1().Secrets(tenant).Get(t.Context(), testAuthSecret, metav1.GetOptions{})
			assert.Errorf(t, err, "the master must NOT appear in %q under dynamic tenancy", tenant)
		}
	})

	// The hook runs on every install AND every upgrade. Regenerating would
	// rotate every derived key with no dual-accept window, so a second run must
	// change nothing.
	t.Run("re-running preserves the existing master", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewClientset()
		nss := []string{"fission", "default"}
		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, nss, testAuthSecret))
		first := masterIn(t, cs, "fission")

		for range 3 {
			require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, nss, testAuthSecret))
		}
		assert.Equal(t, first, masterIn(t, cs, "fission"),
			"re-running must not re-mint the master: that rotates every derived key with no dual-accept window")
		assert.Equal(t, first, masterIn(t, cs, "default"))
	})

	// Upgrade path: an install predating this hook already has the master in
	// the release namespace. Adding a namespace must copy THAT value, not a new
	// one, or the new namespace's pods sign with a key nobody verifies.
	t.Run("an existing master is reused when filling a new namespace", func(t *testing.T) {
		t.Parallel()
		existing := []byte("already-provisioned-master-value")
		cs := fake.NewClientset(&apiv1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testAuthSecret, Namespace: "fission"},
			Data:       map[string][]byte{authSecretKey: existing},
		})
		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, []string{"fission", "default"}, testAuthSecret))

		assert.Equal(t, existing, masterIn(t, cs, "fission"), "the pre-existing master must be left alone")
		assert.Equal(t, existing, masterIn(t, cs, "default"), "the new copy must carry the SAME value, not a fresh one")
	})

	// A copy that exists only outside the release namespace is still the
	// install's master; ignoring it would mint a second one.
	t.Run("a master found in a later namespace is reused", func(t *testing.T) {
		t.Parallel()
		existing := []byte("master-living-in-default")
		cs := fake.NewClientset(&apiv1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testAuthSecret, Namespace: "default"},
			Data:       map[string][]byte{authSecretKey: existing},
		})
		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, []string{"fission", "default"}, testAuthSecret))
		assert.Equal(t, existing, masterIn(t, cs, "fission"))
	})

	t.Run("no namespaces or no name is a no-op", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewClientset()
		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, nil, testAuthSecret))
		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, []string{"fission"}, ""))
		list, err := cs.CoreV1().Secrets(metav1.NamespaceAll).List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		assert.Empty(t, list.Items)
	})

	t.Run("the generated master is long enough to be a key", func(t *testing.T) {
		t.Parallel()
		cs := fake.NewClientset()
		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger, []string{"fission"}, testAuthSecret))
		assert.GreaterOrEqual(t, len(masterIn(t, cs, "fission")), 32,
			"a short master would weaken every derived key")
	})

	t.Run("two runs on empty clusters do not produce the same master", func(t *testing.T) {
		t.Parallel()
		a, b := fake.NewClientset(), fake.NewClientset()
		require.NoError(t, GenerateAuthSecret(t.Context(), a, logger, []string{"fission"}, testAuthSecret))
		require.NoError(t, GenerateAuthSecret(t.Context(), b, logger, []string{"fission"}, testAuthSecret))
		assert.NotEqual(t, masterIn(t, a, "fission"), masterIn(t, b, "fission"),
			"the master must come from a CSPRNG, not a fixed or time-derived value")
	})
}

// TestGenerateAuthSecretConvergence pins what happens when this run is NOT the
// one that created a copy. A create that loses is not evidence that our master
// is right — it is evidence someone else's is. Carrying our own value into the
// remaining namespaces is what makes the set diverge permanently, and the
// symptom is pods signing with a key the control plane will not verify.
func TestGenerateAuthSecretConvergence(t *testing.T) {
	t.Parallel()
	logger := loggerfactory.GetLogger()

	seed := func(t *testing.T, ns string, data map[string][]byte) *fake.Clientset {
		t.Helper()
		cs := fake.NewClientset()
		_, err := cs.CoreV1().Secrets(ns).Create(t.Context(), &apiv1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testAuthSecret, Namespace: ns},
			Data:       data,
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		return cs
	}

	// The ordinary re-run: a master already exists, so every other namespace
	// must receive THAT value, never a freshly minted one. A regenerated master
	// rotates every derived key with no dual-accept window.
	t.Run("adopts the existing master rather than minting a new one", func(t *testing.T) {
		t.Parallel()
		incumbent := []byte("incumbent-master-value")
		cs := seed(t, "fission", map[string][]byte{authSecretKey: incumbent})

		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger,
			[]string{"fission", "default"}, testAuthSecret))

		assert.Equal(t, incumbent, masterIn(t, cs, "fission"), "the incumbent must not be rotated")
		assert.Equal(t, incumbent, masterIn(t, cs, "default"), "the replica must carry the incumbent value")
	})

	// The race this is really about: another run created the FIRST namespace's
	// copy after we read it as absent. Adopting the winner is the only outcome
	// that leaves one master; keeping ours would leave two.
	t.Run("a create lost mid-run converges on the winner", func(t *testing.T) {
		t.Parallel()
		winner := []byte("winner-master-value")
		cs := fake.NewClientset()

		// Let the run see an empty cluster, then plant the winner so the first
		// Create returns AlreadyExists — exactly the interleaving of two hook
		// Jobs started together.
		var planted bool
		cs.PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if planted {
				return false, nil, nil
			}
			planted = true
			ns := action.GetNamespace()
			// Add via the tracker, not the clientset: a nested client call
			// from inside a reactor re-enters the reaction chain.
			require.NoError(t, cs.Tracker().Add(&apiv1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: testAuthSecret, Namespace: ns},
				Data:       map[string][]byte{authSecretKey: winner},
			}))
			return true, nil, k8serrors.NewAlreadyExists(
				schema.GroupResource{Resource: "secrets"}, testAuthSecret)
		})

		require.NoError(t, GenerateAuthSecret(t.Context(), cs, logger,
			[]string{"fission", "default"}, testAuthSecret))

		assert.Equal(t, winner, masterIn(t, cs, "fission"))
		assert.Equal(t, winner, masterIn(t, cs, "default"),
			"the loser must adopt the winner's master, not carry its own into the rest of the set")
	})

	// A Secret that exists but carries no usable key (an aborted rotation, or
	// an ExternalSecret placeholder not yet populated). existingMaster skips it,
	// so without this guard the run would provision every OTHER namespace and
	// leave this one keyless — control-plane pods there stay wedged while the
	// hook exits 0.
	t.Run("a keyless existing Secret fails loudly instead of being provisioned around", func(t *testing.T) {
		t.Parallel()
		for name, data := range map[string]map[string][]byte{
			"missing key": {},
			"empty value": {authSecretKey: {}},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				cs := seed(t, "default", data)
				err := GenerateAuthSecret(t.Context(), cs, logger,
					[]string{"fission", "default"}, testAuthSecret)
				require.Error(t, err, "a keyless master must not be silently provisioned around")
				assert.Contains(t, err.Error(), authSecretKey,
					"the error must name the key an operator has to fix")
			})
		}
	})
}
