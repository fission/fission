// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"sync"
	"testing"
)

// heavyTestMu serializes the package's heaviest multi-pod integration tests
// AMONG THEMSELVES (TestVersionedSpecialize, TestCanaryAliasPromotion,
// TestCanaryAliasRollback) so at most one of them is mid-flight at a time.
// Every other test in this package stays fully parallel, including with the
// heavy ones' non-overlapping windows -- this is not a "make everything
// serial" lock, it only bounds the peak overlap of the tests expensive
// enough to matter.
//
// Each of the three spins up multiple envs/functions/pod fleets (two
// published FunctionVersions each, generation-labeled pods, per-version
// headless Services, and for the canary-alias pair a sustained background
// load generator on top of that). On CI's single-node kind cluster, letting
// all three run concurrently -- on top of the rest of the parallel suite --
// has tipped the node into timeout cascades (observed on the v1.34.8 leg:
// pod scheduling backing up until unrelated tests started missing their
// Eventually windows). Capping their concurrent overlap at one bounds the
// peak pod count these three contribute without giving up the parallelism
// they get against the rest of the suite.
var heavyTestMu sync.Mutex

// acquireHeavySlot takes the shared heavy-test slot for the duration of t and
// releases it automatically via t.Cleanup. Call it first in each heavy test,
// after that test's own skip gates (e.g. framework.Connect,
// f.Images().RequireNode) and before it creates any fixtures -- skipped runs
// must never block on, or hold, the slot.
func acquireHeavySlot(t *testing.T) {
	t.Helper()
	heavyTestMu.Lock()
	t.Cleanup(heavyTestMu.Unlock)
}
