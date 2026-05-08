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
	"strings"
	"time"
)

// VerifierOpts configures the HMAC middleware.
//
// An empty Secret disables enforcement entirely — the middleware passes
// every request through. This is the safe default during initial rollout
// (the env var defaults to empty in deployments that haven't been migrated).
type VerifierOpts struct {
	// Secret is the active shared secret. Empty disables enforcement.
	Secret []byte
	// OldSecret is accepted in addition to Secret during rotation. Zero-length disables.
	OldSecret []byte
	// SkewSec tolerates clock drift between caller and verifier. Defaults to 60.
	SkewSec int64
	// Bypass lists exact-match paths that skip signature checking (e.g. /healthz).
	Bypass []string
	// Now overrides the verifier's clock (defaults to time.Now). Used in tests.
	Now func() time.Time
}

// Verifier returns a middleware constructor that enforces HMAC auth on
// requests, with the body re-injected for downstream handlers to re-read.
func Verifier(opts VerifierOpts) func(http.Handler) http.Handler {
	if opts.SkewSec <= 0 {
		opts.SkewSec = 60
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	bypass := map[string]struct{}{}
	for _, p := range opts.Bypass {
		bypass[p] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Empty Secret disables enforcement (backwards-compat short-circuit).
			if len(opts.Secret) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			if _, ok := bypass[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}

			ts := r.Header.Get(HeaderTimestamp)
			sig := r.Header.Get(HeaderSignature)
			if ts == "" || sig == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			tsNum, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			var body []byte
			if r.Body != nil {
				body, err = io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}

			now := opts.Now().Unix()
			if VerifyWithSkew(opts.Secret, r.Method, r.URL.Path, body, tsNum, sig, now, opts.SkewSec) {
				next.ServeHTTP(w, r)
				return
			}
			if len(opts.OldSecret) > 0 &&
				VerifyWithSkew(opts.OldSecret, r.Method, r.URL.Path, body, tsNum, sig, now, opts.SkewSec) {
				next.ServeHTTP(w, r)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
}
