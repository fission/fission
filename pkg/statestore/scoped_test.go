// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore_test

import (
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/memory"
	"github.com/fission/fission/pkg/utils/metrics"
)

// testMetricsReg backs the meter provider installed for the whole statestore test
// binary, so the OTel counters can be read back via the Prometheus exposition.
var testMetricsReg *prometheus.Registry

func TestMain(m *testing.M) {
	testMetricsReg = prometheus.NewRegistry()
	mp, err := metrics.NewMeterProvider(resource.Default(), testMetricsReg, true)
	if err != nil {
		panic(err)
	}
	otel.SetMeterProvider(mp)
	os.Exit(m.Run())
}

// counterTotal sums every series of the counter family named name (0 if absent).
func counterTotal(t *testing.T, name string) float64 {
	t.Helper()
	fams, err := testMetricsReg.Gather()
	require.NoError(t, err)
	var total float64
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, mtr := range f.GetMetric() {
			total += mtr.GetCounter().GetValue()
		}
	}
	return total
}

func gaugeValue(t *testing.T, name string) float64 {
	t.Helper()
	fams, err := testMetricsReg.Gather()
	require.NoError(t, err)
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, mtr := range f.GetMetric() {
			return mtr.GetGauge().GetValue()
		}
	}
	return 0
}

func scoped(t *testing.T, q statestore.Quota) (statestore.KVStore, statestore.Capabilities) {
	t.Helper()
	inner, err := memory.New()
	require.NoError(t, err)
	caps := statestore.NewScoped(inner, statestore.StaticQuota(q))
	t.Cleanup(func() { _ = caps.Close() }) // deregisters the conservation reporter
	kv, err := caps.KV()
	require.NoError(t, err)
	return kv, caps
}

var sc = statestore.Scope{Namespace: "ns", Owner: "function/f", Keyspace: "k"}

func TestScoped_QuotaMaxValueBytes(t *testing.T) {
	kv, _ := scoped(t, statestore.Quota{MaxValueBytes: 4})
	before := counterTotal(t, "fission_statestore_quota_rejections_total")

	require.NoError(t, kv.Set(t.Context(), sc, "ok", []byte("1234"), statestore.SetOptions{}))
	require.ErrorIs(t, kv.Set(t.Context(), sc, "big", []byte("12345"), statestore.SetOptions{}), statestore.ErrQuotaExceeded)

	require.Equal(t, before+1, counterTotal(t, "fission_statestore_quota_rejections_total"))
}

func TestScoped_QuotaMaxKeys(t *testing.T) {
	kv, _ := scoped(t, statestore.Quota{MaxKeys: 2})
	ctx := t.Context()
	require.NoError(t, kv.Set(ctx, sc, "a", []byte("v"), statestore.SetOptions{}))
	require.NoError(t, kv.Set(ctx, sc, "b", []byte("v"), statestore.SetOptions{}))
	// A third new key is rejected.
	require.ErrorIs(t, kv.Set(ctx, sc, "c", []byte("v"), statestore.SetOptions{}), statestore.ErrQuotaExceeded)
	// Overwriting an existing key does not grow the count, so it is allowed.
	require.NoError(t, kv.Set(ctx, sc, "a", []byte("v2"), statestore.SetOptions{}))
}

func TestScoped_RecordsOps(t *testing.T) {
	kv, _ := scoped(t, statestore.Quota{})
	before := counterTotal(t, "fission_statestore_ops_total")
	require.NoError(t, kv.Set(t.Context(), sc, "a", []byte("v"), statestore.SetOptions{}))
	_, _ = kv.Get(t.Context(), sc, "a")
	require.GreaterOrEqual(t, counterTotal(t, "fission_statestore_ops_total"), before+2)
}

func TestScoped_RecordsErrorsOnRealFailureNotBusinessOutcome(t *testing.T) {
	kv, caps := scoped(t, statestore.Quota{})
	ctx := t.Context()

	// A not-found is a business outcome, not an error.
	beforeErr := counterTotal(t, "fission_statestore_errors_total")
	_, err := kv.Get(ctx, sc, "absent")
	require.ErrorIs(t, err, statestore.ErrNotFound)
	require.Equal(t, beforeErr, counterTotal(t, "fission_statestore_errors_total"))

	// A closed store is a real failure.
	require.NoError(t, caps.Close())
	require.Error(t, kv.Set(ctx, sc, "a", []byte("v"), statestore.SetOptions{}))
	require.Greater(t, counterTotal(t, "fission_statestore_errors_total"), beforeErr)
}

func TestScoped_ConservationDriftGaugeReadsZero(t *testing.T) {
	// Exercise the queue through the scoped wrapper; the drift gauge (T1 gate)
	// must read zero across live reporters.
	_, caps := scoped(t, statestore.Quota{})
	q, err := caps.Queue()
	require.NoError(t, err)
	ctx := t.Context()
	_, err = q.Enqueue(ctx, "asyncinv/ns", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	l, err := q.Lease(ctx, "asyncinv/ns", 1, time.Minute)
	require.NoError(t, err)
	require.NoError(t, q.Ack(ctx, l[0].Receipt))

	require.Zero(t, gaugeValue(t, "fission_statestore_conservation_drift"))
}

func TestConservationStatsDrift(t *testing.T) {
	// Drift computation is the substance of the T1 gate.
	balanced := statestore.ConservationStats{Enqueued: 5, Queued: 2, Leased: 1, Acked: 1, Dead: 1}
	require.Zero(t, balanced.Drift())
	leaky := statestore.ConservationStats{Enqueued: 5, Queued: 1, Leased: 1, Acked: 1, Dead: 1}
	require.EqualValues(t, 1, leaky.Drift())
}
