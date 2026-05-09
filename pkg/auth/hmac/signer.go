/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package hmac

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	// HeaderTimestamp carries the caller's unix-seconds timestamp.
	HeaderTimestamp = "X-Fission-Auth-Timestamp"
	// HeaderSignature carries hex(HMAC-SHA256(secret, canonical)).
	HeaderSignature = "X-Fission-Auth-Signature"
)

// Signer is an http.RoundTripper wrapper that signs every outgoing request
// with the HMAC scheme described in the design at docs/internal-auth/00-design.md.
// It buffers the request body to compute the body hash, then re-injects it.
//
// A Signer constructed with an empty secret short-circuits to pass-through:
// it forwards the request to the inner transport unmodified, without
// reading or buffering the body and without setting any auth headers.
// Callers should still avoid wrapping their transport with an
// empty-secret Signer in production — passing the unsigned transport
// directly to the http.Client skips the indirection entirely.
type Signer struct {
	secret []byte
	rt     http.RoundTripper
	now    func() time.Time
}

// NewSigner returns a Signer that wraps rt. If rt is nil, http.DefaultTransport
// is used; if now is nil, time.Now is used. If secret is empty/nil, the
// Signer is constructed in pass-through mode (see Signer's doc).
func NewSigner(secret []byte, rt http.RoundTripper, now func() time.Time) *Signer {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if now == nil {
		now = time.Now
	}
	return &Signer{secret: secret, rt: rt, now: now}
}

// RoundTrip implements http.RoundTripper.
func (s *Signer) RoundTrip(r *http.Request) (*http.Response, error) {
	// Defence in depth: an empty secret means "internalAuth disabled".
	// Forward without touching the body or headers so a misconfigured
	// caller that constructed an empty-secret Signer doesn't silently
	// emit a bogus signature with an empty-key HMAC (which would still
	// be deterministic and could be replayed). The Verifier short-
	// circuits the same way for an empty Secret, so both ends agree
	// without exchanging any auth metadata.
	if len(s.secret) == 0 {
		return s.rt.RoundTrip(r)
	}
	var body []byte
	if r.Body != nil {
		original := r.Body
		var err error
		body, err = io.ReadAll(original)
		// Close the original body before replacing it: callers that hand
		// in a real *os.File-backed io.ReadCloser would otherwise leak
		// the underlying file descriptor.
		closeErr := original.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	ts := s.now().Unix()
	// Sign over the request-URI (path + raw query) so query parameters
	// like ?id= are bound to the signature. Signing the path alone would
	// let an attacker replay a captured /v1/archive?id=A signature
	// against a different ?id=B within the skew window.
	sig := Sign(s.secret, r.Method, r.URL.RequestURI(), body, ts)
	r.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	r.Header.Set(HeaderSignature, sig)
	return s.rt.RoundTrip(r)
}
