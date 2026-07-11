// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBodyHashPrimitivesMatchSliceForms locks in the backward-compat invariant:
// the *WithBodyHash forms (used by the streaming verifier/signer) produce a
// byte-identical canonical string, signature, and verification result to the
// slice-based Canonical/Sign/Verify when fed hex(sha256(body)).
func TestBodyHashPrimitivesMatchSliceForms(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	bodies := map[string][]byte{
		"nil":   nil,
		"empty": {},
		"small": []byte("hello"),
		"large": make([]byte, 5<<20), // 5 MiB of zeros
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			h := sha256.Sum256(body)
			bodyHashHex := hex.EncodeToString(h[:])
			const (
				method = "POST"
				uri    = "/v1/archive?id=abc"
				ts     = int64(1715000000)
			)

			assert.Equal(t, Canonical(method, uri, body, ts),
				CanonicalFromBodyHash(method, uri, bodyHashHex, ts))

			sliceSig := Sign(secret, method, uri, body, ts)
			assert.Equal(t, sliceSig, SignWithBodyHash(secret, method, uri, bodyHashHex, ts))

			assert.True(t, VerifyWithBodyHash(secret, method, uri, bodyHashHex, ts, sliceSig))
			assert.False(t, VerifyWithBodyHash(secret, method, uri, bodyHashHex, ts, sliceSig+"00"))
		})
	}
}

func TestCanonicalString(t *testing.T) {
	got := Canonical("POST", "/v1/archive", []byte("hello"), 1715000000)
	const want = "POST\n/v1/archive\n2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\n1714999980"
	assert.Equal(t, want, got)
}

func TestSignAndVerify(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	sig := Sign(secret, "GET", "/v1/archive", nil, 1715000000)
	assert.True(t, Verify(secret, "GET", "/v1/archive", nil, 1715000000, sig))
	assert.False(t, Verify(secret, "GET", "/v1/archive", nil, 1715000000, sig+"00"))
}

func TestVerifyClockSkew(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	sig := Sign(secret, "GET", "/v1/archive", nil, 1715000000)
	assert.True(t, VerifyWithSkew(secret, "GET", "/v1/archive", nil, 1715000000, sig, 1715000045, 60))
	assert.False(t, VerifyWithSkew(secret, "GET", "/v1/archive", nil, 1715000000, sig, 1715000061, 60))
}
