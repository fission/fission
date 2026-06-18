// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/auth/hmac"
)

func TestNamespaceAuthSecretDerivesNamespaceKeys(t *testing.T) {
	master := []byte("master-secret-bytes-for-test")
	s := NamespaceAuthSecret(master, "team-a")
	require.NotNil(t, s)

	assert.Equal(t, fv1.TenantAuthKeysSecret, s.Name)
	// Must NOT be the chart's master Secret name, or it would collide with the
	// already-replicated master copy and silently skip writing the keys.
	assert.NotEqual(t, "fission-internal-auth", s.Name, "derived-key Secret must be distinct from the chart master copy")
	assert.Equal(t, "team-a", s.Namespace)
	assert.Equal(t, managedByValue, s.Labels[managedByLabelKey])

	// Keys are the hex-encoded namespace-derived values. Hex matters: the Secret is
	// consumed as ENV VARS, and raw HKDF bytes are not valid UTF-8, which breaks
	// container creation ("string field contains invalid UTF-8").
	assert.Equal(t, []byte(hmac.EncodeKeyForEnv(hmac.DeriveServiceKeyNS(master, hmac.ServiceFetcher, "team-a"))), s.Data["fetcherKey"])
	assert.Equal(t, []byte(hmac.EncodeKeyForEnv(hmac.DeriveServiceKeyNS(master, hmac.ServiceBuilder, "team-a"))), s.Data["builderKey"])
	assert.Equal(t, []byte(hmac.EncodeKeyForEnv(hmac.DeriveServiceKeyNS(master, hmac.ServiceStoragesvc, "team-a"))), s.Data["storageKey"])

	// Regression guard for the invalid-UTF-8 container-creation failure: every
	// value must be valid UTF-8 (so it survives env-var transport) and must
	// round-trip back to the raw derived key.
	for field, val := range s.Data {
		assert.Truef(t, utf8.Valid(val), "field %q must be valid UTF-8 for env-var transport", field)
	}
	assert.Equal(t, hmac.DeriveServiceKeyNS(master, hmac.ServiceFetcher, "team-a"), hmac.DecodeKeyFromEnv(string(s.Data["fetcherKey"])))

	// …and the master itself is NEVER written into a tenant namespace.
	assert.NotContains(t, s.Data, "secret", "the master must never land in a tenant secret")
	for field, val := range s.Data {
		assert.NotEqual(t, master, val, "field %q must be a derived key, not the master", field)
	}
}

func TestNamespaceAuthSecretEmptyMaster(t *testing.T) {
	// No master (internalAuth disabled) → nil; the caller skips writing it.
	assert.Nil(t, NamespaceAuthSecret(nil, "team-a"))
	assert.Nil(t, NamespaceAuthSecret([]byte{}, "team-a"))
}

func TestEnsureNamespaceAuthSecretCreates(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	master := []byte("master-bytes-for-test")

	require.NoError(t, EnsureNamespaceAuthSecret(ctx, c, master, "team-a"))

	s := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fv1.TenantAuthKeysSecret}, s))
	assert.Equal(t, []byte(hmac.EncodeKeyForEnv(hmac.DeriveServiceKeyNS(master, hmac.ServiceFetcher, "team-a"))), s.Data["fetcherKey"])
	assert.Equal(t, managedByValue, s.Labels[managedByLabelKey])
}

func TestEnsureNamespaceAuthSecretEmptyMasterNoop(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()

	require.NoError(t, EnsureNamespaceAuthSecret(ctx, c, nil, "team-a"))

	s := &corev1.Secret{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fv1.TenantAuthKeysSecret}, s)),
		"no secret is written when internalAuth is disabled (empty master)")
}

// TestEnsureNamespaceAuthSecretWritesAlongsideChartMaster is the regression guard
// for the env-seeded-namespace divergence: an existing install already replicated
// the chart's master Secret ("fission-internal-auth") into every function
// namespace. The controller must STILL provision the derived keys there (under
// its own distinct name), or the executor would ns-sign pods whose fetcher only
// ever holds the master — a permanent 401.
func TestEnsureNamespaceAuthSecretWritesAlongsideChartMaster(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	master := []byte("master-bytes-for-test")

	// Pre-seed the chart's master-bearing Secret, exactly as a prior helm render
	// left it in the namespace.
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "fission-internal-auth"},
		Data:       map[string][]byte{"secret": master},
	}))

	require.NoError(t, EnsureNamespaceAuthSecret(ctx, c, master, "team-a"))

	keys := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fv1.TenantAuthKeysSecret}, keys),
		"derived-key Secret must be written even when the chart master copy already exists")
	assert.Equal(t, []byte(hmac.EncodeKeyForEnv(hmac.DeriveServiceKeyNS(master, hmac.ServiceFetcher, "team-a"))), keys.Data["fetcherKey"])
}

func TestEnsureNamespaceAuthSecretIdempotent(t *testing.T) {
	// Re-running is a no-op on the controller's own Secret (AlreadyExists ignored).
	c := newFakeClient(t)
	ctx := t.Context()
	master := []byte("master-bytes-for-test")
	require.NoError(t, EnsureNamespaceAuthSecret(ctx, c, master, "team-a"))
	require.NoError(t, EnsureNamespaceAuthSecret(ctx, c, master, "team-a"), "re-run must not error")
}
