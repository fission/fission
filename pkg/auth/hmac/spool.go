// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

// bodySpool holds a request body the verifier drained while hashing it:
// in memory when it fit within the spool threshold, in a temp file when it
// did not. Exactly one of mem/file is set.
type bodySpool struct {
	hashHex string
	mem     []byte
	file    *os.File
}

// spoolIOError marks a failure of the spool's OWN storage — temp-file
// create/write/seek — as opposed to a failure reading the request body from
// the client. The distinction decides the response class: the verifier maps
// it to 500 (the server's disk is broken, e.g. full or read-only /tmp),
// where a client-read failure stays 401. Without it, a node-level disk fault
// would reject every correctly signed over-threshold upload as Unauthorized
// and send operators debugging HMAC configuration instead of storage.
type spoolIOError struct{ err error }

func (e *spoolIOError) Error() string { return "hmac body spool: " + e.err.Error() }
func (e *spoolIOError) Unwrap() error { return e.err }

// spoolFileWriter tags write-side errors during the spill copy as
// spoolIOError so they are distinguishable from read-side (client) errors
// coming out of the same io.Copy.
type spoolFileWriter struct{ f *os.File }

func (w spoolFileWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if err != nil {
		return n, &spoolIOError{err: err}
	}
	return n, nil
}

// spoolBody drains src, hashing as it reads. Bodies up to threshold bytes
// stay in memory (the historical behavior); anything larger spills to a
// temp file, so the caller's memory stays bounded by the threshold no
// matter how large the body is. On error the returned spool (if any) has
// already been cleaned up.
func spoolBody(src io.Reader, threshold int64) (*bodySpool, error) {
	h := sha256.New()
	tee := io.TeeReader(src, h)
	var buf bytes.Buffer
	// Read one byte past the threshold: landing EOF at or before
	// threshold+1 means the whole body fit and stays in memory.
	_, err := io.CopyN(&buf, tee, threshold+1)
	if errors.Is(err, io.EOF) {
		return &bodySpool{hashHex: hex.EncodeToString(h.Sum(nil)), mem: buf.Bytes()}, nil
	}
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "fission-hmac-body-*")
	if err != nil {
		return nil, &spoolIOError{err: err}
	}
	sp := &bodySpool{file: f}
	if _, err := f.Write(buf.Bytes()); err != nil {
		sp.cleanup()
		return nil, &spoolIOError{err: err}
	}
	if _, err := io.Copy(spoolFileWriter{f: f}, tee); err != nil {
		sp.cleanup()
		return nil, err
	}
	sp.hashHex = hex.EncodeToString(h.Sum(nil))
	return sp, nil
}

// reader returns the spooled body, rewound, for re-injection as r.Body.
// Call it once. Both arms wrap with io.NopCloser so a handler's deferred
// r.Body.Close() cannot close the temp file out from under cleanup, which
// owns the file's lifecycle (io.NopCloser also forwards WriterTo, keeping
// *os.File's copy fast path available to whoever drains the body).
func (s *bodySpool) reader() (io.ReadCloser, error) {
	if s.file == nil {
		return io.NopCloser(bytes.NewReader(s.mem)), nil
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return nil, &spoolIOError{err: err}
	}
	return io.NopCloser(s.file), nil
}

// cleanup closes and removes the temp file, if any. Idempotent; call it
// (deferred) once the downstream handler has returned.
func (s *bodySpool) cleanup() {
	if s.file == nil {
		return
	}
	name := s.file.Name()
	_ = s.file.Close()
	_ = os.Remove(name)
	s.file = nil
}
