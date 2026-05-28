// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fscache

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// These cache service loops run for the lifetime of the cache with no
		// shutdown hook (tracked as a Phase 2 backlog item).
		goleak.IgnoreTopFunction("github.com/fission/fission/pkg/executor/fscache.(*FunctionServiceCache).service"),
		goleak.IgnoreTopFunction("github.com/fission/fission/pkg/executor/fscache.(*PoolCache).service"),
		// Generic cache.Cache backing store also runs a lifetime service loop.
		goleak.IgnoreTopFunction("github.com/fission/fission/pkg/cache.(*Cache[...]).service"),
	)
}
