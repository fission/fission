// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"bytes"
	"encoding/json"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// signedArchiveUpload builds a multipart POST /v1/archive request carrying
// payload, shaped like the storagesvc client (form field "uploadfile" + the
// X-File-Size header), and signs it with the storagesvc-derived key so the HMAC
// verifier admits it. The body the verifier buffers is the multipart wrapper
// around payload, so it is marginally larger than len(payload).
func signedArchiveUpload(t *testing.T, key, payload []byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("uploadfile", "deploy.zip")
	require.NoError(t, err)
	_, err = fw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	body := buf.Bytes()
	req := httptest.NewRequest(http.MethodPost, "/v1/archive", bytes.NewReader(body))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-File-Size", strconv.Itoa(len(payload)))

	ts := time.Now().Unix()
	sig := hmacauth.Sign(key, http.MethodPost, req.URL.RequestURI(), body, ts)
	req.Header.Set(hmacauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(hmacauth.HeaderSignature, sig)
	return req
}

// requireArchiveStored asserts the upload response carries a non-empty archive
// id — i.e. the request reached uploadHandler and putFile actually stored the
// archive, not merely that the verifier returned 200.
func requireArchiveStored(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	var ur UploadResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &ur))
	require.NotEmpty(t, ur.ID, "expected the archive to be stored and an id returned")
}

// TestUploadBodySizeCapConfigurable locks in the fix for #3538: the /v1/archive
// upload body cap is operator-tunable. A correctly signed upload larger than the
// configured cap is rejected with 413 (the regression); a cap above the body —
// or the resolved default — admits and stores it.
func TestUploadBodySizeCapConfigurable(t *testing.T) {
	master := []byte("storagesvc-test-master-key-long-enough")
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)
	payload := bytes.Repeat([]byte("x"), 64*1024) // 64 KiB archive

	// Caps derived from the payload so the bracket holds if the payload size
	// changes: the multipart-wrapped body is >= len(payload), so a half-payload
	// cap rejects it and a several-times-payload cap admits it.
	overCap := int64(len(payload) / 2)
	underCap := int64(len(payload) * 4)

	newHandler := func(t *testing.T, maxUploadBytes int64) http.Handler {
		t.Helper()
		storage := NewLocalStorage(t.TempDir())
		sc, err := MakeStorageClient(logr.Discard(), storage)
		require.NoError(t, err)
		ss := MakeStorageService(logr.Discard(), sc, master, nil, maxUploadBytes)
		return ss.makeHandler()
	}

	t.Run("upload over the configured cap is rejected with 413", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		newHandler(t, overCap).ServeHTTP(rr, signedArchiveUpload(t, keyMaster, payload))
		require.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
	})

	t.Run("a cap above the body admits and stores the upload", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		newHandler(t, underCap).ServeHTTP(rr, signedArchiveUpload(t, keyMaster, payload))
		require.Equal(t, http.StatusOK, rr.Code)
		requireArchiveStored(t, rr)
	})

	t.Run("a zero cap falls back to the default and admits the upload", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		newHandler(t, 0).ServeHTTP(rr, signedArchiveUpload(t, keyMaster, payload))
		require.Equal(t, http.StatusOK, rr.Code)
		requireArchiveStored(t, rr)
	})
}

// TestMaxUploadBytesFromEnv covers the env parsing: MiB → bytes, with unset,
// zero, negative, garbage, unit-suffixed, and overflowing values all falling
// back to 0 (which the verifier resolves to hmacauth.DefaultMaxBodyBytes).
func TestMaxUploadBytesFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int64
	}{
		{"unset", "", 0},
		{"zero", "0", 0},
		{"valid", "512", 512 << 20},
		{"whitespace trimmed", "  300 ", 300 << 20},
		{"negative", "-1", 0},
		{"garbage", "abc", 0},
		{"unit suffix rejected", "512Mi", 0},
		{"overflow falls back to default", strconv.FormatInt(math.MaxInt64, 10), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STORAGE_MAX_ARCHIVE_SIZE_MIB", tc.env)
			require.Equal(t, tc.want, maxUploadBytesFromEnv(logr.Discard()))
		})
	}
}
