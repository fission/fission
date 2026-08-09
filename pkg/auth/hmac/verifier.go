// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// LabeledKey is a candidate verification key tagged with the principal it
// authenticates. When a request verifies against a LabeledKey, that Namespace is
// the request's authenticated principal — recoverable downstream via
// AuthenticatedNamespace. An empty Namespace means an unrestricted principal
// (e.g. a master-derived key held only by the control plane), so a holder of it
// is never scoped to a namespace it merely claimed in a header.
type LabeledKey struct {
	Namespace string
	Key       []byte
}

// authNamespaceCtxKey is the context key under which the verifier stores the
// authenticated principal namespace on a successful match. Unexported so only
// this package can set it; readers use AuthenticatedNamespace.
type authNamespaceCtxKeyType struct{}

var authNamespaceCtxKey authNamespaceCtxKeyType

// AuthenticatedNamespace returns the namespace whose key verified the request,
// as recorded by the verifier middleware on success. ok is false when no
// verifier ran or enforcement was off; ns == "" with ok == true is an
// unrestricted (master) principal. Handlers treat both the unset case and ""
// as unrestricted.
func AuthenticatedNamespace(ctx context.Context) (ns string, ok bool) {
	ns, ok = ctx.Value(authNamespaceCtxKey).(string)
	return ns, ok
}

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
	// SpoolThresholdBytes, when positive, bounds how much of the request body
	// the verifier holds in memory while hashing: bodies up to the threshold
	// stay in memory as before, larger ones spool to a temp file that the
	// re-injected r.Body reads back from — so verifier memory is bounded by
	// the threshold (not MaxBodyBytes) per request. Zero or negative keeps
	// the fully in-memory behavior. The trade is a disk write+read for
	// spooled bodies: set it on listeners where large bodies are real
	// (storagesvc archive uploads) and leave it off on latency-sensitive
	// ones (the router internal listener).
	SpoolThresholdBytes int64
	// Logger receives V(1) messages on each rejection. The zero value is
	// substituted with logr.Discard() at construction so callers that don't
	// care about audit logs don't crash. Rejection log lines deliberately
	// omit the signature and timestamp values to avoid log poisoning.
	Logger logr.Logger
	// KeysFromRequestLabeled, when set, supplies the ordered candidate keys to try
	// for each request, each tagged with the principal namespace recorded (via
	// AuthenticatedNamespace) when that candidate verifies — used by namespace-
	// scoped verifiers that derive the key from a request header (e.g.
	// ServiceVerifierNamespaceFromHeader). It REPLACES Secret/OldSecret for key
	// selection, and a non-nil hook enables enforcement. Empty/nil candidate keys
	// are skipped; an empty namespace label is an unrestricted principal. Lets a
	// multi-tenant verifier authorize on the namespace whose key actually matched,
	// not a caller-controlled header. Keep it cheap — it runs per request.
	KeysFromRequestLabeled func(*http.Request) []LabeledKey
}

// labeledCandidates returns the ordered candidate keys to try for a request,
// each tagged with the principal namespace to record on a match: the per-request
// labeled hook when set, else the static active+rotation pair (unrestricted).
func (o VerifierOpts) labeledCandidates(r *http.Request) []LabeledKey {
	if o.KeysFromRequestLabeled != nil {
		return o.KeysFromRequestLabeled(r)
	}
	return []LabeledKey{{Key: o.Secret}, {Key: o.OldSecret}}
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
			// Empty Secret AND no per-request key hook disables enforcement
			// (backwards-compat short-circuit). In pass-through mode we
			// deliberately do NOT bound the body — the downstream handler's
			// existing limits apply unchanged.
			if len(opts.Secret) == 0 && opts.KeysFromRequestLabeled == nil {
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

			bodyHash := emptyBodyHashHex
			if r.Body != nil {
				if opts.MaxBodyBytes > 0 {
					r.Body = http.MaxBytesReader(w, r.Body, opts.MaxBodyBytes)
				}
				// Spooling disabled resolves to an effectively-infinite
				// threshold: the single drain path below then keeps every
				// body in memory, which is the historical behavior.
				spoolAt := opts.SpoolThresholdBytes
				if spoolAt <= 0 {
					spoolAt = math.MaxInt64 - 1
				}
				spool, spoolErr := spoolBody(r.Body, spoolAt)
				if spoolErr != nil {
					rejectBodyReadError(w, r, opts.Logger, spoolErr)
					return
				}
				// The temp file (if the body spilled) must outlive the
				// downstream handler, which reads the re-injected body
				// from it; remove it once the handler returns.
				defer spool.cleanup()
				body, readerErr := spool.reader()
				if readerErr != nil {
					rejectBodyReadError(w, r, opts.Logger, readerErr)
					return
				}
				bodyHash = spool.hashHex
				r.Body = body
			}

			// Sign over RequestURI (path + raw query) so query parameters
			// like ?id= are bound to the signature. The body was hashed ONCE
			// while draining it; try each candidate key in order (active,
			// then rotation, or the per-request namespace keys) against the
			// same hash; constant-time compare happens inside VerifyFromHash
			// per candidate.
			ru := r.URL.RequestURI()
			for _, c := range opts.labeledCandidates(r) {
				if len(c.Key) > 0 && VerifyFromHash(c.Key, r.Method, ru, bodyHash, tsNum, sig) {
					// Record the principal whose key matched so downstream
					// handlers authorize on the authenticated namespace rather
					// than a caller-controlled header.
					ctx := context.WithValue(r.Context(), authNamespaceCtxKey, c.Namespace)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			opts.Logger.V(1).Info("HMAC verification failed",
				"reason", "signature mismatch",
				"method", r.Method, "path", r.URL.Path,
				"remoteAddr", r.RemoteAddr)
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
}

// rejectBodyReadError writes the response for a body the verifier could not
// drain: an *http.MaxBytesError means the body exceeded MaxBodyBytes (413);
// anything else is a read error (401). Shared by the in-memory and spooling
// body paths so both reject identically.
func rejectBodyReadError(w http.ResponseWriter, r *http.Request, logger logr.Logger, err error) {
	if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
		logger.V(1).Info("HMAC verification failed",
			"reason", "body exceeds MaxBodyBytes",
			"method", r.Method, "path", r.URL.Path,
			"remoteAddr", r.RemoteAddr,
			"limit", maxErr.Limit)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	logger.V(1).Info("HMAC verification failed",
		"reason", "body read error",
		"method", r.Method, "path", r.URL.Path,
		"remoteAddr", r.RemoteAddr)
	w.WriteHeader(http.StatusUnauthorized)
}
