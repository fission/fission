// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// TestGenerateInternalAuthSecretIsIdempotent is the property the whole design
// rests on: a GitOps controller re-runs this on every sync, and re-minting the
// HMAC master would rotate every derived key underneath running pods across
// fail-closed channels.
func TestGenerateInternalAuthSecretIsIdempotent(t *testing.T) {
	t.Parallel()

	const ns, name = "fission", "fission-internal-auth"
	client := fake.NewClientset()
	logger := loggerfactory.GetLogger()

	require.NoError(t, GenerateInternalAuthSecret(t.Context(), client, logger, ns, name))
	first, err := client.CoreV1().Secrets(ns).Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, first.Data["secret"])

	// The generated value must be usable through env transport, which is not
	// binary-safe — so it is base64 of the raw random bytes.
	raw, err := base64.StdEncoding.DecodeString(string(first.Data["secret"]))
	require.NoError(t, err, "the stored master must be base64")
	assert.Len(t, raw, internalAuthMasterBytes)

	// Every subsequent run is a no-op that leaves the value untouched.
	for range 3 {
		require.NoError(t, GenerateInternalAuthSecret(t.Context(), client, logger, ns, name))
	}
	after, err := client.CoreV1().Secrets(ns).Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, first.Data["secret"], after.Data["secret"],
		"re-running the generator must never rotate an existing master")
	assert.Equal(t, first.ResourceVersion, after.ResourceVersion,
		"re-running the generator must not write at all when the secret exists")
}

// TestGenerateInternalAuthSecretLeavesUserManagedValueAlone covers the
// existingSecret path colliding with the chart name: whatever is already there
// wins, and the generator never reads it.
func TestGenerateInternalAuthSecretLeavesUserManagedValueAlone(t *testing.T) {
	t.Parallel()

	const ns, name = "fission", "my-auth"
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       map[string][]byte{"secret": []byte("operator-chosen")},
	}
	client := fake.NewClientset(existing)

	require.NoError(t, GenerateInternalAuthSecret(t.Context(), client, loggerfactory.GetLogger(), ns, name))

	got, err := client.CoreV1().Secrets(ns).Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []byte("operator-chosen"), got.Data["secret"])
}

func TestGenerateInternalAuthSecretGeneratesDistinctValues(t *testing.T) {
	t.Parallel()

	logger := loggerfactory.GetLogger()
	seen := make(map[string]struct{}, 5)
	for range 5 {
		client := fake.NewClientset()
		require.NoError(t, GenerateInternalAuthSecret(t.Context(), client, logger, "fission", "s"))
		got, err := client.CoreV1().Secrets("fission").Get(t.Context(), "s", metav1.GetOptions{})
		require.NoError(t, err)
		seen[string(got.Data["secret"])] = struct{}{}
	}
	assert.Len(t, seen, 5, "each fresh install must get its own master")
}
