// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeReporter struct{ stats ConservationStats }

func (f fakeReporter) ConservationStats(context.Context) ConservationStats { return f.stats }

// The conservation gauge must actually detect a nonzero drift — the memory
// driver derives Enqueued and the state counts from one loop so its drift is 0
// by construction, which would make the gauge vacuous. A real driver computes the
// counts from separate queries that can disagree; this proves the summing path
// surfaces such a disagreement.
func TestSumConservationDriftDetectsImbalance(t *testing.T) {
	dereg := registerConservationReporter(fakeReporter{ConservationStats{
		Enqueued: 5, Queued: 1, Leased: 1, Acked: 1, Dead: 1, // drift = 5 - 4 = 1
	}})
	t.Cleanup(dereg)
	require.GreaterOrEqual(t, sumConservationDrift(context.Background()), int64(1))
}

func TestRegisterConservationReporterDeregisters(t *testing.T) {
	ctx := context.Background()
	before := sumConservationDrift(ctx)
	dereg := registerConservationReporter(fakeReporter{ConservationStats{Enqueued: 3}}) // drift = 3
	require.Equal(t, before+3, sumConservationDrift(ctx))
	dereg()
	require.Equal(t, before, sumConservationDrift(ctx), "deregister removes the reporter")
}
