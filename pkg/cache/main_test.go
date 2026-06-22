// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// expiryService runs for the lifetime of a Cache configured with an
		// expiry, with no shutdown hook (tracked as a Phase 2 backlog item).
		goleak.IgnoreTopFunction("github.com/fission/fission/pkg/cache.(*Cache[...]).expiryService"),
	)
}
