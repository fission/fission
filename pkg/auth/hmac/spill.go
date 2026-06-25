// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
// temp file and MUST be called on every path. The net/http server closes the
// ORIGINAL captured request body, not the spillReader the verifier re-injects,
// so the verifier owns this lifetime and defers Close() on every exit. Close is
// idempotent.
type spillReader struct {
	hasher hash.Hash
	file   *os.File  // non-nil once spilled; the staged body lives here
	reader io.Reader // replay source (bytes.Reader or the seeked file)
}

// newSpillReader reads src to completion, teeing every byte through SHA-256 and
// staging it in memory until it would exceed threshold, after which it spills to
// an os.CreateTemp file in dir (empty dir → os.TempDir()). A read error
// (including *http.MaxBytesError from a MaxBytesReader-wrapped src) is returned
// after cleaning up any temp file, so the caller can map it to the right status
// before any handler runs.
func newSpillReader(src io.Reader, threshold int64, dir string) (*spillReader, error) {
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
				f, ferr := os.CreateTemp(dir, "fission-hmac-body-*")
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
		sr.reader = bytes.NewReader(mem.Bytes())
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
	rmErr := os.Remove(name)
	if os.IsNotExist(rmErr) {
		rmErr = nil
	}
	return errors.Join(closeErr, rmErr)
}
