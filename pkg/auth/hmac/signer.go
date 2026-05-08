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
// with the HMAC scheme described in the design at docs/internal-auth/00-design.md. It buffers the request body
// to compute the body hash, then re-injects it.
type Signer struct {
	secret []byte
	rt     http.RoundTripper
	now    func() time.Time
}

// NewSigner returns a Signer that wraps rt. If rt is nil, http.DefaultTransport
// is used; if now is nil, time.Now is used.
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
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	ts := s.now().Unix()
	sig := Sign(s.secret, r.Method, r.URL.Path, body, ts)
	r.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	r.Header.Set(HeaderSignature, sig)
	return s.rt.RoundTrip(r)
}
