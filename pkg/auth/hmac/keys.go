/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package hmac

import (
	"crypto/hkdf"
	"crypto/sha256"
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
)

// derivedKeyLength is the size of every per-service signing key. 32
// bytes (256 bits) matches SHA-256's digest length — HMAC-SHA256
// keys at or below the digest length avoid the inner padding to the
// 64-byte block size — and the master secret's entropy budget.
const derivedKeyLength = 32

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
	if len(master) == 0 {
		return nil
	}
	info := KeyVersion + ":" + string(service)
	// hkdf.Key only fails when keyLength exceeds the underlying
	// hash's output bound (255 * HashSize). 32 bytes for SHA-256 is
	// well inside that limit, so the error is unreachable.
	key, err := hkdf.Key(sha256.New, master, nil, info, derivedKeyLength)
	if err != nil {
		// Should never happen with sha256 + 32 bytes; surface as a
		// programmer error if it ever does.
		panic("hmac.DeriveServiceKey: hkdf.Key failed: " + err.Error())
	}
	return key
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
