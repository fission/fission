// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

func TestWriteStateTokenFile(t *testing.T) {
	dir := t.TempDir()
	f := &Fetcher{sharedVolumePath: dir}
	loadReq := FunctionLoadRequest{
		FunctionMetadata: &metav1.ObjectMeta{Name: "counter", Namespace: "user-ns"},
		StateKeyspace:    "carts",
	}

	t.Run("derives the exact keyspace token", func(t *testing.T) {
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "test-master")
		require.NoError(t, f.writeStateTokenFile(loadReq))

		got, err := os.ReadFile(filepath.Join(dir, StateTokenFileName))
		require.NoError(t, err)
		want := hmacauth.EncodeKeyForEnv(hmacauth.DeriveStateKeyspaceKey([]byte("test-master"), "user-ns", "carts"))
		assert.Equal(t, want, string(got))
	})

	t.Run("no master secret: dev placeholder", func(t *testing.T) {
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "")
		require.NoError(t, f.writeStateTokenFile(loadReq))
		got, err := os.ReadFile(filepath.Join(dir, StateTokenFileName))
		require.NoError(t, err)
		assert.Equal(t, "dev-unauthenticated", string(got))
	})

	t.Run("missing metadata is an error", func(t *testing.T) {
		assert.Error(t, f.writeStateTokenFile(FunctionLoadRequest{StateKeyspace: "carts"}))
	})
}
