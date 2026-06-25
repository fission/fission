// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
)

// spillChunkSize is the read granularity while staging a body.
const spillChunkSize = 32 * 1024

// spillReader stages a request body while computing its SHA-256, holding it in
// memory up to a threshold and spilling to a temp file beyond it, then replays
// the staged bytes for the downstream handler. It lets the HMAC verifier check
// a body-bound signature on an arbitrarily large body with bounded memory.
//
// Lifecycle: newSpillReader consumes src fully (staging + hashing); the caller
// then reads BodyHashHex() and may Read() the body back. Close() releases the
// temp file and MUST be called on every path — the http server closes the
// re-injected request body on the success path, and the verifier closes it
// itself on any non-handoff exit (signature mismatch, etc.). Close is idempotent.
type spillReader struct {
	hasher hash.Hash
	file   *os.File  // non-nil once spilled; the staged body lives here
	buf    []byte    // retained in-memory body when never spilled
	reader io.Reader // replay source (bytes.Reader or the seeked file)
}

// newSpillReader reads src to completion, teeing every byte through SHA-256 and
// staging it in memory until it would exceed threshold, after which it spills to
// an os.CreateTemp file. A read error (including *http.MaxBytesError from a
// MaxBytesReader-wrapped src) is returned after cleaning up any temp file, so
// the caller can map it to the right status before any handler runs.
func newSpillReader(src io.Reader, threshold int64) (*spillReader, error) {
	sr := &spillReader{hasher: sha256.New()}
	mem := &bytes.Buffer{}
	chunk := make([]byte, spillChunkSize)
	var size int64
	for {
		n, err := src.Read(chunk)
		if n > 0 {
			sr.hasher.Write(chunk[:n])
			size += int64(n)
			if sr.file == nil && size > threshold {
				f, ferr := os.CreateTemp("", "fission-hmac-body-*")
				if ferr != nil {
					return nil, ferr
				}
				sr.file = f
				if _, werr := f.Write(mem.Bytes()); werr != nil {
					_ = sr.discard()
					return nil, werr
				}
				mem.Reset()
			}
			if sr.file != nil {
				if _, werr := sr.file.Write(chunk[:n]); werr != nil {
					_ = sr.discard()
					return nil, werr
				}
			} else {
				mem.Write(chunk[:n])
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = sr.discard()
			return nil, err
		}
	}
	if sr.file != nil {
		if _, err := sr.file.Seek(0, io.SeekStart); err != nil {
			_ = sr.discard()
			return nil, err
		}
		sr.reader = sr.file
	} else {
		sr.buf = mem.Bytes()
		sr.reader = bytes.NewReader(sr.buf)
	}
	return sr, nil
}

// BodyHashHex returns hex(SHA-256(body)) of the fully-staged body.
func (sr *spillReader) BodyHashHex() string {
	return hex.EncodeToString(sr.hasher.Sum(nil))
}

func (sr *spillReader) Read(p []byte) (int, error) {
	if sr.reader == nil {
		return 0, io.EOF
	}
	return sr.reader.Read(p)
}

// Close releases the temp file (if any). Idempotent.
func (sr *spillReader) Close() error {
	return sr.discard()
}

func (sr *spillReader) discard() error {
	if sr.file == nil {
		return nil
	}
	name := sr.file.Name()
	closeErr := sr.file.Close()
	sr.file = nil
	sr.reader = nil
	if rmErr := os.Remove(name); rmErr != nil && !os.IsNotExist(rmErr) {
		return rmErr
	}
	return closeErr
}
