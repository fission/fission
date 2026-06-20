// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyTree(t *testing.T) {
	t.Run("single file", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "f.txt")
		require.NoError(t, os.WriteFile(src, []byte("hello"), 0o644))
		dst := filepath.Join(dir, "out", "copy.txt")

		require.NoError(t, copyTree(src, dst))
		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, "hello", string(got))
	})

	t.Run("directory tree", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		require.NoError(t, os.MkdirAll(filepath.Join(src, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("b"), 0o644))
		dst := filepath.Join(dir, "dst")

		require.NoError(t, copyTree(src, dst))
		a, err := os.ReadFile(filepath.Join(dst, "a.txt"))
		require.NoError(t, err)
		assert.Equal(t, "a", string(a))
		b, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
		require.NoError(t, err)
		assert.Equal(t, "b", string(b))
	})
}

func TestPostBuild(t *testing.T) {
	t.Run("success decodes the build response", func(t *testing.T) {
		var gotReq buildRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
			_ = json.NewEncoder(w).Encode(buildResponse{ArtifactFilename: "src-abc123", BuildLogs: "built ok"})
		}))
		defer srv.Close()

		resp, err := postBuild(t.Context(), srvPort(t, srv), buildRequest{SrcPkgFilename: "src", BuildCommand: "build.sh"})
		require.NoError(t, err)
		assert.Equal(t, "src-abc123", resp.ArtifactFilename)
		assert.Equal(t, "built ok", resp.BuildLogs)
		assert.Equal(t, "src", gotReq.SrcPkgFilename)
		assert.Equal(t, "build.sh", gotReq.BuildCommand)
	})

	t.Run("non-2xx surfaces the build error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "compile failed", http.StatusInternalServerError)
		}))
		defer srv.Close()

		_, err := postBuild(t.Context(), srvPort(t, srv), buildRequest{SrcPkgFilename: "src"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "compile failed")
	})
}

// srvPort extracts the TCP port an httptest.Server is listening on.
func srvPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	return srv.Listener.Addr().(*net.TCPAddr).Port
}
