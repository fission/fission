// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	// HeaderNamespace carries the caller's namespace for namespace-scoped
	// channels (storagesvc): the verifier derives the per-namespace key from it.
	// It is NOT part of the signed canonical string — it does not need to be,
	// because the key is bound to the claimed namespace: a caller can only
	// produce a valid signature for the namespace whose key it actually holds, so
	// claiming a different namespace just makes the verifier derive a key the
	// caller cannot sign with. See ServiceVerifierNamespaceFromHeader.
	HeaderNamespace = "X-Fission-Auth-Namespace"
)

// Signer is an http.RoundTripper wrapper that signs every outgoing request
// with the HMAC scheme described in the design at docs/internal-auth/00-design.md.
// It streams the body hash from GetBody when the request has one, and only
// falls back to buffering the body (re-injecting it afterwards) when it
// doesn't — see bodyHashForRequest.
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
	// On error the request body has already been closed (exactly once) by
	// bodyHashForRequest — the inner transport never runs for the request,
	// so the RoundTripper close-even-on-error contract is honored there,
	// where each path knows whether the body is still open.
	bodyHash, err := bodyHashForRequest(r)
	if err != nil {
		return nil, err
	}
	ts := s.now().Unix()
	// Sign over the request-URI (path + raw query) so query parameters
	// like ?id= are bound to the signature. Signing the path alone would
	// let an attacker replay a captured /v1/archive?id=A signature
	// against a different ?id=B within the skew window.
	sig := SignFromHash(s.secret, r.Method, r.URL.RequestURI(), bodyHash, ts)
	r.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	r.Header.Set(HeaderSignature, sig)
	return s.rt.RoundTrip(r)
}

// bodyHashForRequest returns hex(SHA256(body)) for the request's body,
// preferring a streaming read over buffering.
//
// Contract: the streaming path signs the bytes GetBody yields while the
// transport streams r.Body — it relies on the http.Request invariant that
// GetBody returns a fresh copy of the same content as Body. A caller that
// breaks that invariant (hand-built request with divergent hooks, or a body
// partially consumed after construction) produces a signature over bytes
// that never hit the wire; the verifier rejects it with 401 on every attempt
// including rewound retries. Divergence fails closed — it can never get
// unsigned bytes accepted.
//
// When GetBody is set (net/http populates it automatically for requests
// built from *bytes.Buffer / *bytes.Reader / *strings.Reader, and
// file-backed callers can set it themselves), the hash is streamed from a
// fresh GetBody reader and the request's Body/GetBody are left untouched —
// the transport streams the original body, so the signer holds no copy of
// it. This matters for archive uploads, whose multipart body previously got
// a second full in-memory copy here just to be hashed.
//
// Without GetBody, the body is buffered and re-injected (Body AND GetBody —
// rewind-aware retry layers, net/http's replay after a broken connection and
// pkg/utils/httpretry, refuse to retry a request whose GetBody is nil, so
// leaving it unset silently makes signed requests un-retryable; and a naive
// caller re-submitting the same *http.Request would re-read the consumed
// reader and send — re-signed on the second pass — an EMPTY body, which the
// verifier happily accepts: silent success with an empty payload rather
// than a 401).
//
// On error the request body (if any) has been closed EXACTLY once: the
// streaming branch closes it before returning (the transport will never run
// to close it), and the buffering branch has already closed the original as
// part of draining it. Callers must not close again — io.Closer declares
// behavior after the first Close undefined, and a caller-supplied body with
// a non-idempotent Close (pooled buffers, refcounts) must not see two.
func bodyHashForRequest(r *http.Request) (string, error) {
	if r.GetBody != nil {
		fresh, err := r.GetBody()
		if err != nil {
			closeRequestBody(r)
			return "", err
		}
		h := sha256.New()
		_, err = io.Copy(h, fresh)
		closeErr := fresh.Close()
		if err != nil {
			closeRequestBody(r)
			return "", err
		}
		if closeErr != nil {
			closeRequestBody(r)
			return "", closeErr
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	if r.Body == nil {
		return emptyBodyHashHex, nil
	}
	original := r.Body
	body, err := io.ReadAll(original)
	// Close the original body before replacing it: callers that hand
	// in a real *os.File-backed io.ReadCloser would otherwise leak
	// the underlying file descriptor.
	closeErr := original.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return BodyHashHex(body), nil
}

// closeRequestBody closes r.Body if present, ignoring the result — used on
// error paths where the read/hash error (not a close error) is what the
// caller needs to see.
func closeRequestBody(r *http.Request) {
	if r.Body != nil {
		_ = r.Body.Close()
	}
}
