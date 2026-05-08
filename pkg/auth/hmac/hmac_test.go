/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package hmac

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
