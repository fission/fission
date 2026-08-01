// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestAdoptSecretNames(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"":        nil,
		"   ":     nil,
		"a":       {"a"},
		"a,b":     {"a", "b"},
		" a , b ": {"a", "b"},
		"a,,b,":   {"a", "b"},
		// The SPACE-separated form is what the chart actually renders
		// (`join " "`), so these rows are the ones that pin the parser to its
		// real input. A comma-only parser returns the single bogus name
		// "a b" here, which RBAC rejects and which fails every default
		// install — and comma-separated rows alone would never catch it.
		"fission-internal-auth fission-webhook-certs": {"fission-internal-auth", "fission-webhook-certs"},
		"fission-internal-auth router":                {"fission-internal-auth", "router"},
		"fission-internal-auth,router":                {"fission-internal-auth", "router"},
	}
	for in, want := range cases {
		// ElementsMatch rather than Equal: the caller only checks len(), so an
		// empty-vs-nil slice distinction is not a behaviour this pins.
		assert.ElementsMatchf(t, want, adoptSecretNames(in), "input %q", in)
	}
}

func TestAdoptSecretsForKeep(t *testing.T) {
	t.Parallel()

	const ns = "fission"
	logger := loggerfactory.GetLogger()

	secret := func(name string, annotations map[string]string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: annotations},
			Data:       map[string][]byte{"secret": []byte("master-value")},
		}
	}

	t.Run("annotates a live secret without touching its data", func(t *testing.T) {
		t.Parallel()
		client := fake.NewClientset(secret("fission-internal-auth", nil))
		require.NoError(t, AdoptSecretsForKeep(t.Context(), client, logger, []string{ns}, []string{"fission-internal-auth"}))

		got, err := client.CoreV1().Secrets(ns).Get(t.Context(), "fission-internal-auth", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "keep", got.Annotations[keepPolicyAnnotation],
			"without this annotation Helm prunes the secret when the template stops rendering it")
		assert.Equal(t, []byte("master-value"), got.Data["secret"],
			"adoption must never disturb the value — re-minting the HMAC master breaks every running pod")
	})

	t.Run("preserves existing annotations", func(t *testing.T) {
		t.Parallel()
		client := fake.NewClientset(secret("router", map[string]string{"other": "kept"}))
		require.NoError(t, AdoptSecretsForKeep(t.Context(), client, logger, []string{ns}, []string{"router"}))

		got, err := client.CoreV1().Secrets(ns).Get(t.Context(), "router", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "keep", got.Annotations[keepPolicyAnnotation])
		assert.Equal(t, "kept", got.Annotations["other"], "a merge patch must not drop sibling annotations")
	})

	t.Run("absent secret is a no-op", func(t *testing.T) {
		t.Parallel()
		// A fresh install has none of these yet; the hook still runs.
		client := fake.NewClientset()
		require.NoError(t, AdoptSecretsForKeep(t.Context(), client, logger, []string{ns}, []string{"fission-internal-auth", "router"}))
	})

	t.Run("idempotent across repeated upgrades", func(t *testing.T) {
		t.Parallel()
		client := fake.NewClientset(secret("fission-webhook-certs", nil))
		for range 3 {
			require.NoError(t, AdoptSecretsForKeep(t.Context(), client, logger, []string{ns}, []string{"fission-webhook-certs"}))
		}
		got, err := client.CoreV1().Secrets(ns).Get(t.Context(), "fission-webhook-certs", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "keep", got.Annotations[keepPolicyAnnotation])
	})

	t.Run("adopts the same names across every replicated namespace", func(t *testing.T) {
		t.Parallel()
		// Replicated copies must be adopted too, not just the release
		// namespace's — see AdoptSecretsForKeep for the silent failure that
		// missing them causes.
		client := fake.NewClientset(
			secret("fission-internal-auth", nil),
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "fission-internal-auth", Namespace: "default"}},
		)
		require.NoError(t, AdoptSecretsForKeep(t.Context(), client, logger,
			[]string{ns, "default"}, []string{"fission-internal-auth"}))

		for _, in := range []string{ns, "default"} {
			got, err := client.CoreV1().Secrets(in).Get(t.Context(), "fission-internal-auth", metav1.GetOptions{})
			require.NoError(t, err, in)
			assert.Equalf(t, "keep", got.Annotations[keepPolicyAnnotation], "namespace %s", in)
		}
	})

	t.Run("empty names and namespaces are skipped", func(t *testing.T) {
		t.Parallel()
		client := fake.NewClientset()
		require.NoError(t, AdoptSecretsForKeep(t.Context(), client, logger, []string{ns, ""}, []string{"", "  "}))
	})
}
