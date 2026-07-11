// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package hmac implements Fission's internal HMAC application-layer auth
// scheme described in the design at docs/internal-auth/00-design.md. The
// canonical string used as the HMAC input is:
//
//	<METHOD>\n
//	<REQUEST-URI>\n
//	<SHA256_HEX(BODY)>\n
//	<UNIX_MINUTE>
//
// where REQUEST-URI is path + raw query string (see net/url.URL.RequestURI),
// and UNIX_MINUTE = floor(unix_seconds / 60) * 60. Including the query
// string binds parameters such as ?id= to the signature so a captured
// signed GET /v1/archive?id=A cannot be replayed against ?id=B within
// the skew window. The body hash binds the signature to the bytes; the
// rounded minute keeps the input stable across short retries while still
// rejecting hour-old replays via the timestamp header (see VerifyWithSkew).
//
// Sign / Verify accept the canonical-string components positionally —
// callers must pass the request URI (path + query), not just the path,
// for query-bound services. The Signer / Verifier middlewares already
// do this via r.URL.RequestURI(); call sites that re-implement signing
// outside those middlewares MUST do the same.
package hmac

import (
	cryptohmac "crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Canonical returns the canonical string that is fed into HMAC-SHA256.
// timestampSec is rounded down to the nearest minute. The `requestURI`
// argument should be path + raw query (e.g. r.URL.RequestURI()), not
// just the path — query parameters MUST be bound to the signature for
// services like storagesvc that key on `?id=`.
func Canonical(method, requestURI string, body []byte, timestampSec int64) string {
	bodyHash := sha256.Sum256(body)
	return CanonicalFromBodyHash(method, requestURI, hex.EncodeToString(bodyHash[:]), timestampSec)
}

// CanonicalFromBodyHash is Canonical for callers that have already computed
// hex(SHA-256(body)) — e.g. by streaming the body through a hasher rather than
// holding it in memory. `bodyHashHex` MUST be the lowercase hex SHA-256 of the
// exact body bytes; the result is byte-identical to Canonical for that body.
func CanonicalFromBodyHash(method, requestURI, bodyHashHex string, timestampSec int64) string {
	minute := timestampSec - (timestampSec % 60)
	return fmt.Sprintf("%s\n%s\n%s\n%d", method, requestURI, bodyHashHex, minute)
}

// Sign returns hex(HMAC-SHA256(secret, Canonical(...))). `requestURI`
// is path + raw query (see Canonical).
func Sign(secret []byte, method, requestURI string, body []byte, timestampSec int64) string {
	bodyHash := sha256.Sum256(body)
	return SignWithBodyHash(secret, method, requestURI, hex.EncodeToString(bodyHash[:]), timestampSec)
}

// SignWithBodyHash is Sign for callers that supply hex(SHA-256(body)) directly
// (see CanonicalFromBodyHash). The signature is identical to Sign over the same body.
func SignWithBodyHash(secret []byte, method, requestURI, bodyHashHex string, timestampSec int64) string {
	mac := cryptohmac.New(sha256.New, secret)
	mac.Write([]byte(CanonicalFromBodyHash(method, requestURI, bodyHashHex, timestampSec)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify is a constant-time signature check at the request's own timestamp.
// Use VerifyWithSkew for clock-skew tolerance; bare Verify is intended for
// callers that have already validated freshness (e.g. unit tests).
// `requestURI` is path + raw query (see Canonical).
func Verify(secret []byte, method, requestURI string, body []byte, timestampSec int64, sig string) bool {
	bodyHash := sha256.Sum256(body)
	return VerifyWithBodyHash(secret, method, requestURI, hex.EncodeToString(bodyHash[:]), timestampSec, sig)
}

// VerifyWithBodyHash is Verify for callers that supply hex(SHA-256(body))
// directly (see CanonicalFromBodyHash). It is a constant-time check.
func VerifyWithBodyHash(secret []byte, method, requestURI, bodyHashHex string, timestampSec int64, sig string) bool {
	want := SignWithBodyHash(secret, method, requestURI, bodyHashHex, timestampSec)
	return cryptohmac.Equal([]byte(want), []byte(sig))
}

// VerifyWithSkew accepts the signature if the request timestamp is within
// `skewSec` of the verifier's clock (`nowSec`). The signature itself is
// always computed over the request's own timestamp — skew is only a clock
// freshness check. `requestURI` is path + raw query (see Canonical).
func VerifyWithSkew(secret []byte, method, requestURI string, body []byte,
	timestampSec int64, sig string, nowSec, skewSec int64) bool {
	if abs(nowSec-timestampSec) > skewSec {
		return false
	}
	return Verify(secret, method, requestURI, body, timestampSec, sig)
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
