// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"fmt"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func dockerConfigSecret(name, ns, registry, user, password string) *apiv1.Secret {
	cfg := fmt.Sprintf(`{"auths":{"%s":{"username":"%s","password":"%s"}}}`, registry, user, password)
	return &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Type:       apiv1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{apiv1.DockerConfigJsonKey: []byte(cfg)},
	}
}

func TestKeychainResolvesSAAndExplicitSecrets(t *testing.T) {
	t.Parallel()
	const ns = "fn-ns"

	client := k8sfake.NewSimpleClientset(
		// The fetcher SA carries an imagePullSecret for registry A.
		&apiv1.ServiceAccount{
			ObjectMeta:       metav1.ObjectMeta{Name: "fission-fetcher", Namespace: ns},
			ImagePullSecrets: []apiv1.LocalObjectReference{{Name: "sa-pull-secret"}},
		},
		dockerConfigSecret("sa-pull-secret", ns, "registry-a.example.com", "sa-user", "sa-pass"),
		// An explicit per-package pull secret for registry B.
		dockerConfigSecret("pkg-pull-secret", ns, "registry-b.example.com", "pkg-user", "pkg-pass"),
	)

	kc, err := Keychain(t.Context(), client, ns, "fission-fetcher",
		[]apiv1.LocalObjectReference{{Name: "pkg-pull-secret"}})
	require.NoError(t, err)

	for _, tc := range []struct {
		registry string
		user     string
		password string
	}{
		{"registry-a.example.com", "sa-user", "sa-pass"},
		{"registry-b.example.com", "pkg-user", "pkg-pass"},
	} {
		repo, err := name.NewRepository(tc.registry + "/code/hello")
		require.NoError(t, err)
		auth, err := kc.Resolve(repo)
		require.NoError(t, err)
		cfg, err := auth.Authorization()
		require.NoError(t, err)
		assert.Equal(t, tc.user, cfg.Username, "registry %s", tc.registry)
		assert.Equal(t, tc.password, cfg.Password, "registry %s", tc.registry)
	}
}

func TestKeychainFallsBackToAnonymous(t *testing.T) {
	t.Parallel()
	client := k8sfake.NewSimpleClientset(
		&apiv1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "fission-fetcher", Namespace: "ns"},
		},
	)
	kc, err := Keychain(t.Context(), client, "ns", "fission-fetcher", nil)
	require.NoError(t, err)

	repo, err := name.NewRepository("registry.example.com/code/hello")
	require.NoError(t, err)
	auth, err := kc.Resolve(repo)
	require.NoError(t, err)
	cfg, err := auth.Authorization()
	require.NoError(t, err)
	assert.Empty(t, cfg.Username)
	assert.Empty(t, cfg.Password)
}

func TestKeychainMissingServiceAccountStillWorks(t *testing.T) {
	t.Parallel()
	// A namespace without the fetcher SA (e.g. user-managed namespaces) must
	// not fail keychain construction — pulls just run with explicit secrets
	// plus anonymous fallback.
	client := k8sfake.NewSimpleClientset(
		dockerConfigSecret("pkg-pull-secret", "ns", "registry-b.example.com", "u", "p"),
	)
	kc, err := Keychain(t.Context(), client, "ns", "fission-fetcher",
		[]apiv1.LocalObjectReference{{Name: "pkg-pull-secret"}})
	require.NoError(t, err)

	repo, err := name.NewRepository("registry-b.example.com/code/hello")
	require.NoError(t, err)
	auth, err := kc.Resolve(repo)
	require.NoError(t, err)
	cfg, err := auth.Authorization()
	require.NoError(t, err)
	assert.Equal(t, "u", cfg.Username)
}
