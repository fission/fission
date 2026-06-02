// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestResolveSecretReferences(t *testing.T) {
	t.Parallel()
	kc := k8sfake.NewClientset(&v1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}})

	t.Run("empty names returns nil", func(t *testing.T) {
		t.Parallel()
		refs, err := ResolveSecretReferences(t.Context(), kc, nil, "ns", true, true)
		require.NoError(t, err)
		assert.Nil(t, refs)
	})

	t.Run("no check builds refs in the given namespace", func(t *testing.T) {
		t.Parallel()
		refs, err := ResolveSecretReferences(t.Context(), kc, []string{"a", "b"}, "spec-ns", false, false)
		require.NoError(t, err)
		require.Len(t, refs, 2)
		assert.Equal(t, "spec-ns", refs[0].Namespace)
		assert.Equal(t, "a", refs[0].Name)
	})

	t.Run("existing secret passes the check", func(t *testing.T) {
		t.Parallel()
		refs, err := ResolveSecretReferences(t.Context(), kc, []string{"s"}, "ns", true, true)
		require.NoError(t, err)
		require.Len(t, refs, 1)
	})

	t.Run("missing secret warns but still builds (strict)", func(t *testing.T) {
		t.Parallel()
		refs, err := ResolveSecretReferences(t.Context(), kc, []string{"missing"}, "ns", true, true)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, "missing", refs[0].Name)
	})
}

func TestResolveConfigMapReferences(t *testing.T) {
	t.Parallel()
	kc := k8sfake.NewClientset(&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}})

	t.Run("empty names returns nil", func(t *testing.T) {
		t.Parallel()
		refs, err := ResolveConfigMapReferences(t.Context(), kc, nil, "ns", true, false)
		require.NoError(t, err)
		assert.Nil(t, refs)
	})

	t.Run("builds refs and tolerates a missing configmap", func(t *testing.T) {
		t.Parallel()
		refs, err := ResolveConfigMapReferences(t.Context(), kc, []string{"c", "missing"}, "ns", true, false)
		require.NoError(t, err)
		require.Len(t, refs, 2)
		assert.Equal(t, "ns", refs[1].Namespace)
	})
}
