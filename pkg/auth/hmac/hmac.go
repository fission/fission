/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package hmac implements Fission's internal HMAC application-layer auth
// scheme described in the design at docs/internal-auth/00-design.md. The canonical string used as the HMAC input
// is:
//
//	<METHOD>\n
//	<PATH>\n
//	<SHA256_HEX(BODY)>\n
//	<UNIX_MINUTE>
//
// where UNIX_MINUTE = floor(unix_seconds / 60) * 60. The body hash binds
// the signature to the bytes; the rounded minute keeps the input stable
// across short retries while still rejecting hour-old replays via the
// timestamp header (see VerifyWithSkew).
package hmac

import (
	cryptohmac "crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Canonical returns the canonical string that is fed into HMAC-SHA256.
// timestampSec is rounded down to the nearest minute.
func Canonical(method, path string, body []byte, timestampSec int64) string {
	bodyHash := sha256.Sum256(body)
	minute := timestampSec - (timestampSec % 60)
	return fmt.Sprintf("%s\n%s\n%s\n%d", method, path, hex.EncodeToString(bodyHash[:]), minute)
}

// Sign returns hex(HMAC-SHA256(secret, Canonical(...))).
func Sign(secret []byte, method, path string, body []byte, timestampSec int64) string {
	mac := cryptohmac.New(sha256.New, secret)
	mac.Write([]byte(Canonical(method, path, body, timestampSec)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify is a constant-time signature check at the request's own timestamp.
// Use VerifyWithSkew for clock-skew tolerance; bare Verify is intended for
// callers that have already validated freshness (e.g. unit tests).
func Verify(secret []byte, method, path string, body []byte, timestampSec int64, sig string) bool {
	want := Sign(secret, method, path, body, timestampSec)
	return cryptohmac.Equal([]byte(want), []byte(sig))
}

// VerifyWithSkew accepts the signature if the request timestamp is within
// `skewSec` of the verifier's clock (`nowSec`). The signature itself is
// always computed over the request's own timestamp — skew is only a clock
// freshness check.
func VerifyWithSkew(secret []byte, method, path string, body []byte,
	timestampSec int64, sig string, nowSec, skewSec int64) bool {
	if abs(nowSec-timestampSec) > skewSec {
		return false
	}
	return Verify(secret, method, path, body, timestampSec, sig)
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
