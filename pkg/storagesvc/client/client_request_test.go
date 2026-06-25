// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referenceMultipart builds the multipart body multipart.Writer would produce
// for the given boundary, so a streamed body can be asserted byte-identical.
func referenceMultipart(t *testing.T, boundary, fileName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	require.NoError(t, mw.SetBoundary(boundary))
	fw, err := mw.CreateFormFile("uploadfile", fileName)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return buf.Bytes()
}

// TestNewUploadRequestStreamsSeekableBody: for a seekable body, newUploadRequest
// produces a request whose Body streams the exact multipart bytes (not a
// buffered copy), with a re-readable GetBody, correct ContentLength, and the
// X-File-Size header — without ever holding the body in a bytes.Buffer.
func TestNewUploadRequestStreamsSeekableBody(t *testing.T) {
	content := bytes.Repeat([]byte("payload-"), 4096) // 32 KiB
	req, err := newUploadRequest(context.Background(), "http://storage/archive", "deploy.zip", bytes.NewReader(content), int64(len(content)))
	require.NoError(t, err)
	require.NotNil(t, req.GetBody, "streamed upload must set GetBody for the signer")

	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err)
	want := referenceMultipart(t, params["boundary"], "deploy.zip", content)

	// Body streams the exact multipart bytes.
	got, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, want, got, "streamed multipart must match multipart.Writer output")
	assert.Equal(t, int64(len(want)), req.ContentLength)
	assert.Equal(t, strconv.Itoa(len(content)), req.Header.Get("X-File-Size"))

	// GetBody yields a fresh, identical stream (re-reads the seekable source).
	rc, err := req.GetBody()
	require.NoError(t, err)
	regot, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, want, regot, "GetBody must reproduce the body byte-for-byte")
}

// nonSeekableReader wraps a Reader so it is not an io.ReadSeeker, forcing the
// buffered fallback.
type nonSeekableReader struct{ r io.Reader }

func (n nonSeekableReader) Read(p []byte) (int, error) { return n.r.Read(p) }

// TestNewUploadRequestBuffersNonSeekableBody: a non-seekable body falls back to
// the buffered path but still produces a correct, re-readable multipart request.
func TestNewUploadRequestBuffersNonSeekableBody(t *testing.T) {
	content := []byte("non-seekable-content")
	req, err := newUploadRequest(context.Background(), "http://storage/archive", "x.zip", nonSeekableReader{bytes.NewReader(content)}, int64(len(content)))
	require.NoError(t, err)

	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err)
	want := referenceMultipart(t, params["boundary"], "x.zip", content)

	got, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, strconv.Itoa(len(content)), req.Header.Get("X-File-Size"))
}
