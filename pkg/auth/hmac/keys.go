// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"
)

// KeyVersion is mixed into the HKDF info string so a future revision
// of the per-service derivation scheme can be rolled out without
// colliding with previously-issued keys. Bumping this constant
// invalidates every signature in flight; treat it as a wire-format
// version. See docs/internal-auth/00-design.md.
const KeyVersion = "fission-internal-v1"

// Service identifies a logical communication channel. Each named
// channel uses an independently-derived signing key so a leak of one
// channel's runtime memory cannot forge requests on a different
// channel. Service identifiers are part of the HKDF info string and
// must remain stable across releases — adding a new identifier is a
// new channel; renaming an existing one is a wire-format break.
type Service string

const (
	// ServiceStoragesvc gates storagesvc /v1/archive (advisory 2).
	ServiceStoragesvc Service = "storagesvc"
	// ServiceFetcher gates the in-pod cmd/fetcher endpoints
	// (/fetch, /upload, /clean, /specialize).
	ServiceFetcher Service = "fetcher"
	// ServiceBuilder gates the cmd/builder /build endpoint.
	ServiceBuilder Service = "builder"
	// ServiceExecutor gates the executor's HTTP API
	// (/v2/getServiceForFunction, /v2/tap, /v2/error, etc.).
	ServiceExecutor Service = "executor"
	// ServiceRouterInternal gates the router's internal listener
	// (advisory 4) that hosts /fission-function/<ns>/<name>.
	ServiceRouterInternal Service = "router-internal"
	// ServiceStatestore gates the embedded statestore's capability API
	// (pkg/statestore/httpapi) served by the --statestorePort head (RFC-0021).
	ServiceStatestore Service = "statestore"
)

// derivedKeyLength is the size of every per-service signing key. 32
// bytes (256 bits) matches SHA-256's digest length — HMAC-SHA256
// keys at or below the digest length avoid the inner padding to the
// 64-byte block size — and the master secret's entropy budget.
const derivedKeyLength = 32

// deriveKey is the shared HKDF-SHA256 derivation behind DeriveServiceKey and
// DeriveServiceKeyNS. Keeping it single-sourced guarantees the two derivations
// stay byte-compatible in everything but the info string — same hash, nil salt,
// derivedKeyLength — which is exactly the property the "the two never collide"
// guarantee (and the unchanged master-scoped keys) relies on. Returns nil for an
// empty master, so callers treat a nil/empty master as "internalAuth disabled".
func deriveKey(master []byte, info string) []byte {
	if len(master) == 0 {
		return nil
	}
	// hkdf.Key only fails when keyLength exceeds the underlying hash's output
	// bound (255 * HashSize). 32 bytes for SHA-256 is well inside that limit, so
	// the error is unreachable; surface it as a programmer error if it ever fires.
	key, err := hkdf.Key(sha256.New, master, nil, info, derivedKeyLength)
	if err != nil {
		panic("hmac.deriveKey: hkdf.Key failed: " + err.Error())
	}
	return key
}

// DeriveServiceKey returns the per-service signing key derived from
// the master secret via HKDF-SHA256. Both signer and verifier call
// this with the same service identifier, so the derived key matches
// end-to-end.
//
// The derivation is deterministic: given the same master and service,
// every caller gets the same key. Different services produce
// different keys, so a leak of one channel's derived key reveals
// neither the master nor any other channel's key.
//
// Returns nil for an empty master. Callers should treat a nil/empty
// master as "internalAuth disabled" and skip wrapping their transport
// or registering the verifier middleware entirely (see
// pkg/storagesvc/client.MakeClient and pkg/storagesvc/storagesvc.go::Start
// for the canonical patterns). The Signer also short-circuits to a
// pass-through when constructed with an empty secret as defence in
// depth, but relying on that is discouraged because the Verifier
// behaves the same — and the cleaner upgrade path is for both ends to
// agree by both being unsigned, not by one side signing with an empty
// key while the other passes through.
func DeriveServiceKey(master []byte, service Service) []byte {
	return deriveKey(master, KeyVersion+":"+string(service))
}

// ServiceSigner returns a Signer whose key is derived from `master`
// for the given service. When master is empty the returned Signer
// short-circuits to pass-through (no headers added, no body buffering)
// — but in practice callers should check emptiness and skip wrapping
// the transport entirely so the http.Client falls back to its inner
// transport directly. See pkg/storagesvc/client.MakeClient for the
// canonical pattern.
func ServiceSigner(master []byte, service Service, rt http.RoundTripper, now func() time.Time) *Signer {
	return NewSigner(DeriveServiceKey(master, service), rt, now)
}

// ServiceVerifier returns a verifier middleware constructor whose
// active and rotation keys are derived from `masterSecret` and
// `masterOldSecret` for the given service. The provided VerifierOpts
// has its Secret/OldSecret fields overwritten with the derived
// values; other fields (SkewSec, Bypass, MaxBodyBytes, Logger) are
// preserved.
//
// An empty masterSecret disables enforcement (the underlying
// Verifier short-circuits to pass-through). An empty masterOldSecret
// disables rotation acceptance — the active key is the only valid
// signer.
func ServiceVerifier(masterSecret, masterOldSecret []byte, service Service, opts VerifierOpts) func(http.Handler) http.Handler {
	opts.Secret = DeriveServiceKey(masterSecret, service)
	opts.OldSecret = DeriveServiceKey(masterOldSecret, service)
	return Verifier(opts)
}

// DeriveServiceKeyNS is DeriveServiceKey scoped additionally to a namespace, for
// the multi-namespace channels (fetcher, builder, storagesvc) whose signing key
// is mounted into a tenant pod: a leak of one tenant's key cannot forge requests
// for another tenant or for the master-derived control-plane channels.
//
// The namespace is appended to the HKDF info as "<KeyVersion>:<service>:<ns>".
// Because the plain service derivation stops at "<KeyVersion>:<service>", the two
// never collide — so introducing namespace scoping leaves every existing
// master-scoped key byte-for-byte unchanged (no KeyVersion bump, no in-flight
// signature breakage). Returns nil for an empty master, like DeriveServiceKey.
func DeriveServiceKeyNS(master []byte, service Service, namespace string) []byte {
	return deriveKey(master, KeyVersion+":"+string(service)+":"+namespace)
}

// ServiceSignerNS is ServiceSigner for a namespace-scoped channel. The signer
// holds the master and derives the namespace key on the fly, so a control-plane
// component (which holds the master) can sign for whichever tenant namespace it
// is acting on.
func ServiceSignerNS(master []byte, service Service, namespace string, rt http.RoundTripper, now func() time.Time) *Signer {
	return NewSigner(DeriveServiceKeyNS(master, service, namespace), rt, now)
}

// VerifierFromKey builds a verifier from already-derived key bytes rather than a
// master — for a tenant pod (fetcher/builder) that is provisioned with only its
// own namespace-scoped derived key and never sees the master. key/oldKey are the
// active and rotation derived keys (oldKey may be nil). The signing counterpart
// is NewSigner, which already takes a raw key.
func VerifierFromKey(key, oldKey []byte, opts VerifierOpts) func(http.Handler) http.Handler {
	opts.Secret = key
	opts.OldSecret = oldKey
	return Verifier(opts)
}

// VerifierFromKeyOrMaster builds the verifier middleware for a tenant sidecar
// (fetcher/builder) that may hold EITHER its own per-namespace derived key OR
// nothing but the master. It centralizes the one security decision both sidecars
// make so they can't drift: when a non-empty derived `key` is provided (a
// dynamic-tenancy pod, provisioned with only its namespace key and never the
// master), verify with that key and its rotation `keyOld`; otherwise derive
// `service`'s key from `master`/`masterOld` (the static-namespace install, where
// an empty master is pass-through). The package stays env-free — callers read
// their own env vars and pass the decoded bytes.
func VerifierFromKeyOrMaster(key, keyOld, master, masterOld []byte, service Service, opts VerifierOpts) func(http.Handler) http.Handler {
	if len(key) > 0 {
		return VerifierFromKey(key, keyOld, opts)
	}
	return ServiceVerifier(master, masterOld, service, opts)
}

// EncodeKeyForEnv encodes a derived key for transport through a Kubernetes Secret
// that a pod consumes as an ENVIRONMENT VARIABLE. Raw HKDF output is binary and is
// not valid UTF-8, and env-var values must be — the kubelet/containerd reject the
// pod at container creation with "grpc: error unmarshalling request: string field
// contains invalid UTF-8". Hex keeps the value printable, exact, and round-trips
// via DecodeKeyFromEnv. (The master key needs no encoding: it is randAlphaNum and
// is only ever derived-from in-process, never surfaced raw to a pod.)
func EncodeKeyForEnv(key []byte) string {
	if len(key) == 0 {
		return ""
	}
	return hex.EncodeToString(key)
}

// DecodeKeyFromEnv reverses EncodeKeyForEnv for a derived key read from an
// environment variable. Empty yields nil. A value that is not valid hex is
// returned as its raw bytes, so a key set by hand (or any pre-encoding caller)
// still works — the provisioner always hex-encodes, so this path is defensive.
func DecodeKeyFromEnv(s string) []byte {
	if s == "" {
		return nil
	}
	if b, err := hex.DecodeString(s); err == nil {
		return b
	}
	return []byte(s)
}

// ServiceVerifierNamespaceFromHeader is a verifier for a namespace-scoped channel
// served by a control-plane component that handles many tenants (storagesvc): for
// each request it derives the per-namespace key from the caller's HeaderNamespace,
// and also accepts the master-derived (non-namespace) key so an old, pre-migration
// client that signs master-derived is still accepted during the upgrade window
// (dual-accept). It holds the master (it is control-plane) and derives per request.
//
// The header is unsigned and does not need to be bound into the canonical: a
// caller can only produce a valid signature with the namespace key it actually
// holds, so claiming a different namespace just makes the verifier derive a key
// the caller cannot sign with → rejection.
//
// Candidates are labeled with the principal they authenticate so a handler can
// authorize on the namespace whose key actually matched (AuthenticatedNamespace),
// not the raw header: an ns-key match reports the header namespace, a master-key
// match reports "" (unrestricted) regardless of any header the caller set — so a
// master holder is never mis-scoped to a namespace it merely claimed.
func ServiceVerifierNamespaceFromHeader(masterSecret, masterOldSecret []byte, service Service, opts VerifierOpts) func(http.Handler) http.Handler {
	opts.KeysFromRequestLabeled = func(r *http.Request) []LabeledKey {
		keys := make([]LabeledKey, 0, 4)
		if ns := r.Header.Get(HeaderNamespace); ns != "" {
			keys = append(keys,
				LabeledKey{Namespace: ns, Key: DeriveServiceKeyNS(masterSecret, service, ns)},
				LabeledKey{Namespace: ns, Key: DeriveServiceKeyNS(masterOldSecret, service, ns)},
			)
		}
		// Dual-accept the master-derived key for pre-migration clients that sign
		// master-derived and send no namespace header. Labeled "" (unrestricted):
		// only the control plane holds the master.
		keys = append(keys,
			LabeledKey{Namespace: "", Key: DeriveServiceKey(masterSecret, service)},
			LabeledKey{Namespace: "", Key: DeriveServiceKey(masterOldSecret, service)},
		)
		return keys
	}
	return Verifier(opts)
}
