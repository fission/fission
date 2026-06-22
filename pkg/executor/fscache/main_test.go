// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fscache

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// The fscache caches no longer run any background goroutine (the actor
	// service loops were replaced with locks), so no goleak ignores are needed.
	goleak.VerifyTestMain(m)
}
