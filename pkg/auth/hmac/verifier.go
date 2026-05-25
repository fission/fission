// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// DefaultMaxBodyBytes caps the size of a request body the verifier will buffer
// in memory. 256 MiB comfortably exceeds any realistic Fission archive while
// bounding the memory cost of a malicious unsigned upload (without this cap an
// attacker can stream an arbitrary number of bytes before we reject with 401).
const DefaultMaxBodyBytes int64 = 256 << 20

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
	// MaxBodyBytes caps the body bytes the verifier will buffer when enforcement
	// is active. Zero means DefaultMaxBodyBytes; a negative value disables the
	// cap (NOT recommended outside tests). Bodies larger than the cap are
	// rejected with 413 Request Entity Too Large before signature verification.
	// The cap is only applied when enforcement is on (Secret non-empty); the
	// pass-through short-circuit deliberately leaves the body untouched.
	MaxBodyBytes int64
	// Logger receives V(1) messages on each rejection. The zero value is
	// substituted with logr.Discard() at construction so callers that don't
	// care about audit logs don't crash. Rejection log lines deliberately
	// omit the signature and timestamp values to avoid log poisoning.
	Logger logr.Logger
}

// Replay note: a signature presented twice within the SkewSec window will pass
// twice. Nonce tracking would require a shared store across replicas and is
// out of scope for the design at docs/internal-auth/00-design.md; see the "Limitations" section of that RFC.

// Verifier returns a middleware constructor that enforces HMAC auth on
// requests, with the body re-injected for downstream handlers to re-read.
func Verifier(opts VerifierOpts) func(http.Handler) http.Handler {
	if opts.SkewSec <= 0 {
		opts.SkewSec = 60
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	// logr.Logger is a value type; the zero value's IsZero() reports unset.
	if opts.Logger.IsZero() {
		opts.Logger = logr.Discard()
	}
	bypass := map[string]struct{}{}
	for _, p := range opts.Bypass {
		bypass[p] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Empty Secret disables enforcement (backwards-compat short-circuit).
			// In pass-through mode we deliberately do NOT bound the body — the
			// downstream handler's existing limits apply unchanged.
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
				opts.Logger.V(1).Info("HMAC verification failed",
					"reason", "missing headers",
					"method", r.Method, "path", r.URL.Path,
					"remoteAddr", r.RemoteAddr)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			tsNum, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
			if err != nil {
				opts.Logger.V(1).Info("HMAC verification failed",
					"reason", "unparseable timestamp",
					"method", r.Method, "path", r.URL.Path,
					"remoteAddr", r.RemoteAddr)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			// Check freshness BEFORE buffering the body. A stale-timestamp
			// request with a multi-MB body would otherwise force the
			// verifier to allocate up to MaxBodyBytes before rejecting,
			// turning a no-op rejection into a memory amplification.
			now := opts.Now().Unix()
			if abs(now-tsNum) > opts.SkewSec {
				opts.Logger.V(1).Info("HMAC verification failed",
					"reason", "stale timestamp",
					"method", r.Method, "path", r.URL.Path,
					"remoteAddr", r.RemoteAddr)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			var body []byte
			if r.Body != nil {
				if opts.MaxBodyBytes > 0 {
					r.Body = http.MaxBytesReader(w, r.Body, opts.MaxBodyBytes)
				}
				body, err = io.ReadAll(r.Body)
				if err != nil {
					var maxErr *http.MaxBytesError
					if errors.As(err, &maxErr) {
						opts.Logger.V(1).Info("HMAC verification failed",
							"reason", "body exceeds MaxBodyBytes",
							"method", r.Method, "path", r.URL.Path,
							"remoteAddr", r.RemoteAddr,
							"limit", maxErr.Limit)
						w.WriteHeader(http.StatusRequestEntityTooLarge)
						return
					}
					opts.Logger.V(1).Info("HMAC verification failed",
						"reason", "body read error",
						"method", r.Method, "path", r.URL.Path,
						"remoteAddr", r.RemoteAddr)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}

			// Sign over RequestURI (path + raw query) so query parameters
			// like ?id= are bound to the signature.
			ru := r.URL.RequestURI()
			if Verify(opts.Secret, r.Method, ru, body, tsNum, sig) {
				next.ServeHTTP(w, r)
				return
			}
			if len(opts.OldSecret) > 0 &&
				Verify(opts.OldSecret, r.Method, ru, body, tsNum, sig) {
				next.ServeHTTP(w, r)
				return
			}
			opts.Logger.V(1).Info("HMAC verification failed",
				"reason", "signature mismatch",
				"method", r.Method, "path", r.URL.Path,
				"remoteAddr", r.RemoteAddr)
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
}
