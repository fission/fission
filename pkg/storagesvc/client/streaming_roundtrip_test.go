// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/storagesvc"
)

// TestUploadStreamingRoundTripThroughSpillVerifier closes the full loop: a
// file-backed Upload streams the multipart body, the signer hashes it via
// GetBody, the ServiceStoragesvc-derived spill verifier stream-verifies it, and
// the handler parses the multipart and recovers the exact file bytes.
func TestUploadStreamingRoundTripThroughSpillVerifier(t *testing.T) {
	master := []byte("storagesvc-e2e-master-key-32bytes")
	content := bytes.Repeat([]byte("archive-data-"), 8192) // ~104 KiB, above the spill threshold

	var gotFile []byte
	verifier := hmacauth.ServiceVerifierNamespaceFromHeader(master, nil, hmacauth.ServiceStoragesvc,
		hmacauth.VerifierOpts{SkewSec: 60, Now: time.Now, SpillThreshold: 16 * 1024})
	srv := httptest.NewServer(verifier(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(0))
		f, _, err := r.FormFile("uploadfile")
		require.NoError(t, err)
		defer f.Close()
		gotFile, err = io.ReadAll(f)
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(w).Encode(storagesvc.UploadResponse{ID: "stored-id"}))
	})))
	defer srv.Close()

	fp := filepath.Join(t.TempDir(), "deploy.zip")
	require.NoError(t, os.WriteFile(fp, content, 0o600))

	c := MakeClient(srv.URL, master)
	id, err := c.Upload(t.Context(), fp, nil)
	require.NoError(t, err)
	assert.Equal(t, "stored-id", id)
	assert.Equal(t, content, gotFile, "server must receive the streamed file intact")
}
