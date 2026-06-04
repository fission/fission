// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestEnsureInternalAuthSecret(t *testing.T) {
	logger := loggerfactory.GetLogger()

	t.Run("creates the secret from env in a fresh namespace", func(t *testing.T) {
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "s3cr3t")
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET_OLD", "old")
		kc := k8sfake.NewClientset()

		EnsureInternalAuthSecret(t.Context(), kc, logger, "tenant-ns")

		got, err := kc.CoreV1().Secrets("tenant-ns").Get(t.Context(), InternalAuthSecretName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, []byte("s3cr3t"), got.Data["secret"])
		assert.Equal(t, []byte("old"), got.Data["oldSecret"])
	})

	t.Run("no-op when internal auth is disabled (no env)", func(t *testing.T) {
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "")
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET_OLD", "")
		kc := k8sfake.NewClientset()

		EnsureInternalAuthSecret(t.Context(), kc, logger, "tenant-ns")

		_, err := kc.CoreV1().Secrets("tenant-ns").Get(t.Context(), InternalAuthSecretName, metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err), "must not create a secret when internal auth is disabled")
	})

	t.Run("disabled DELETES a leftover secret (self-heal on toggle-off)", func(t *testing.T) {
		// Simulate a copy left behind from when internal auth was enabled.
		kc := k8sfake.NewClientset(&apiv1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: InternalAuthSecretName, Namespace: "tenant-ns"},
			Data:       map[string][]byte{"secret": []byte("stale")},
		})
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "")
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET_OLD", "")

		EnsureInternalAuthSecret(t.Context(), kc, logger, "tenant-ns")

		_, err := kc.CoreV1().Secrets("tenant-ns").Get(t.Context(), InternalAuthSecretName, metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err),
			"a leftover secret must be deleted when internal auth is off, else fetchers keep enforcing HMAC")
	})

	t.Run("updates only when the value drifts", func(t *testing.T) {
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "v2")
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET_OLD", "")
		kc := k8sfake.NewClientset()

		EnsureInternalAuthSecret(t.Context(), kc, logger, "tenant-ns")
		first, err := kc.CoreV1().Secrets("tenant-ns").Get(t.Context(), InternalAuthSecretName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, []byte("v2"), first.Data["secret"])

		// Same value again: idempotent, no spurious change.
		EnsureInternalAuthSecret(t.Context(), kc, logger, "tenant-ns")
		same, err := kc.CoreV1().Secrets("tenant-ns").Get(t.Context(), InternalAuthSecretName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, first.ResourceVersion, same.ResourceVersion, "unchanged value must not trigger an update")

		// Drifted value: updates in place.
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "v3")
		EnsureInternalAuthSecret(t.Context(), kc, logger, "tenant-ns")
		updated, err := kc.CoreV1().Secrets("tenant-ns").Get(t.Context(), InternalAuthSecretName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, []byte("v3"), updated.Data["secret"])
	})

	t.Run("empty namespace is a no-op", func(t *testing.T) {
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "s")
		kc := k8sfake.NewClientset()
		EnsureInternalAuthSecret(t.Context(), kc, logger, "")
		// nothing to assert beyond not panicking / not erroring
	})
}
