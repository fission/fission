// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// TestClassifyOutcomeAndCounterParity pins that the recorder's buckets and the
// gate counters classify identically — they share classifyOutcome, and this
// guards the mapping from that one function into the counter fields the error
// budget reads.
func TestClassifyOutcomeAndCounterParity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		o      loadgen.Observation
		bucket string
	}{
		{"2xx is ok", loadgen.Observation{Status: 200}, bucketOK},
		{"no response is transport", loadgen.Observation{Status: 0, Err: errors.New("dial tcp: connection refused")}, bucketTransport},
		{"client timeout is transport", loadgen.Observation{Status: 0, Err: context.DeadlineExceeded, Latency: 30 * time.Second}, bucketTransport},
		{"non-2xx with attribution header", loadgen.Observation{Status: 500, HeaderVal: "executor", Err: errors.New("unexpected status 500")}, bucketAttributed},
		{"499 with header still attributed", loadgen.Observation{Status: 499, HeaderVal: "router", Err: errors.New("unexpected status 499")}, bucketAttributed},
		{"non-2xx without header", loadgen.Observation{Status: 404, Err: errors.New("unexpected status 404")}, bucketUnattributed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.bucket, classifyOutcome(tc.o))

			var c upgradeCounters
			require.True(t, c.observe(tc.o), "an open counter must count")
			counts := map[string]int64{
				bucketOK:           c.counts.OK,
				bucketTransport:    c.counts.Transport,
				bucketAttributed:   c.counts.Attributed,
				bucketUnattributed: c.counts.Unattributed,
			}
			for bucket, n := range counts {
				want := int64(0)
				if bucket == tc.bucket {
					want = 1
				}
				assert.Equalf(t, want, n, "counter %s after a %s observation", bucket, tc.bucket)
			}

			c.closing.Store(true)
			assert.False(t, c.observe(tc.o),
				"a closing counter must refuse AND tell the caller, so no other sink records what it did not count")
		})
	}
}

// TestRecorderBucketsAndCaps pins timeline bucketing, the failure-record cap
// with its drop counter (totals must keep counting past the cap), and
// concurrent safety (meaningful under -race).
func TestRecorderBucketsAndCaps(t *testing.T) {
	t.Parallel()

	// t0 in the past puts observations into a deterministic non-zero bucket:
	// 12s ago → bucket index 2 at 5s resolution.
	rec := newUpgradeRecorder(time.Now().Add(-12 * time.Second))
	rec.observe("poolmgr", loadgen.Observation{Status: 200})
	rec.observe("poolmgr", loadgen.Observation{Status: 500, HeaderVal: "executor", Err: errors.New("boom")})

	// Cap: flood past maxFailureRecords and require the overflow to be
	// counted, not silently discarded — and the TIMELINE to keep counting.
	var wg sync.WaitGroup
	const goroutines = 8
	const perGoroutine = (maxFailureRecords + 100) / goroutines
	const flood = perGoroutine * goroutines
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				rec.observe("newdeploy", loadgen.Observation{Status: 503, HeaderVal: "executor", Err: errors.New("x")})
			}
		}()
	}
	wg.Wait()

	// Assert through the artifact — the schema consumers actually read —
	// rather than the recorder's internals.
	dir := t.TempDir()
	rec.writeArtifact(dir)
	att := readAttribution(t, dir)
	require.GreaterOrEqual(t, len(att.Timeline["poolmgr"]), 3, "observations 12s after t0 must land in bucket index 2")
	assert.Equal(t, int64(1), att.Timeline["poolmgr"][2].OK)
	assert.Equal(t, int64(1), att.Timeline["poolmgr"][2].Attributed)
	assert.Len(t, att.Failures, maxFailureRecords, "detail records stop at the cap")
	assert.Positive(t, att.Dropped["newdeploy"], "overflow must be accounted, not silent")
	var timelineTotal int64
	for _, b := range att.Timeline["newdeploy"] {
		timelineTotal += b.Attributed
	}
	assert.Equal(t, int64(flood), timelineTotal,
		"the timeline must count every observation even after detail records cap out")

	// Drop accounting must not conflate the two meters: lifecycle-event
	// overflow reported as failure_records_dropped would read, on a red run,
	// as "failures were not captured" — the opposite of the truth.
	var res report.ScenarioResult
	rec.metaInto(&res)
	assert.Contains(t, res.Meta, "failure_records_dropped")
	assert.NotContains(t, res.Meta, "lifecycle_events_dropped",
		"no lifecycle events were dropped in this test")
}

// TestRecorderSummaryAndArtifact renders the human summary and the sidecar
// artifact from one populated recorder and checks the load-bearing content:
// phase markers interleaved, the first failing bucket flagged, failures listed,
// and the artifact JSON round-tripping with the same counts.
func TestRecorderSummaryAndArtifact(t *testing.T) {
	t.Parallel()

	rec := newUpgradeRecorder(time.Now().Add(-40 * time.Second))
	rec.mark("load-start")
	rec.mark("upgrade-cmd-start")
	rec.event("pod", "fission/executor-abc", "deleted", "control-plane")
	rec.observe("poolmgr", loadgen.Observation{Status: 200})
	rec.observe("poolmgr", loadgen.Observation{Status: 0, Err: errors.New("read tcp: connection reset by peer"), Latency: 120 * time.Millisecond})

	var sb strings.Builder
	rec.logSummary(&sb, []string{"poolmgr", "newdeploy"})
	out := sb.String()
	assert.Contains(t, out, "[load-start]")
	assert.Contains(t, out, "[upgrade-cmd-start]")
	assert.Contains(t, out, "[pod fission/executor-abc deleted control-plane]")
	assert.Contains(t, out, "<- first failing bucket")
	assert.Contains(t, out, "=== failures", "captured failures must be listed, not only counted")
	assert.Contains(t, out, "connection reset", "the failure detail lines must carry the error")

	dir := t.TempDir()
	rec.writeArtifact(dir)
	att := readAttribution(t, dir)
	assert.Len(t, att.Failures, 1)
	assert.Equal(t, bucketTransport, att.Failures[0].Bucket)
	assert.Len(t, att.Phases, 2)
	require.Contains(t, att.Timeline, "poolmgr")

	// Meta: phase markers + drop accounting survive into the result schema
	// the workflow reads.
	var res report.ScenarioResult
	rec.metaInto(&res)
	assert.Contains(t, res.Meta, "phase_load_start")
	assert.Contains(t, res.Meta, "phase_upgrade_cmd_start_offset_s")
}

// TestWaitControlPlaneRolledRecordsTransitions drives the rollout poll against
// a fake clientset whose Deployment status converges over time, inside a
// synctest bubble so the 5s poll interval costs nothing. It pins three
// contracts: rolled fires once; converged does NOT latch (a regression emits
// `regressed` and the re-convergence a second `converged` — latching would pin
// a later failure window to a control plane the timeline claims was settled);
// and a Deployment helm never changed emits no availability events at all.
func TestWaitControlPlaneRolledRecordsTransitions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dep := func(name string, gen, observed int64, avail int32) *appsv1.Deployment {
			return &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fission", Generation: gen},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: observed,
					UpdatedReplicas:    avail,
					AvailableReplicas:  avail,
				},
			}
		}
		// router has already rolled (generation moved past the snapshot) but
		// is not yet converged; executor is new (absent from the snapshot) and
		// stays unconverged until the end — it is what HOLDS the poll open
		// while the router runs its converge -> regress -> re-converge cycle;
		// webhook is untouched by the upgrade and fully available throughout.
		kube := fake.NewClientset(
			dep("router", 2, 1, 0),
			dep("executor", 1, 1, 0),
			dep("webhook", 3, 3, 1),
		)
		gensBefore := map[string]int64{"router": 1, "webhook": 3}

		var mu sync.Mutex
		events := map[string]int{}
		record := func(dp, event string) {
			mu.Lock()
			defer mu.Unlock()
			events[dp+"/"+event]++
		}

		done := make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			done <- waitControlPlaneRolled(ctx, kube, "fission", gensBefore, time.Minute, record)
		}()

		// Let several polls observe the unconverged router, converge it, let a
		// poll see that, REGRESS it (pod died), then re-converge.
		time.Sleep(12 * time.Second)
		update := func(d *appsv1.Deployment) {
			_, err := kube.AppsV1().Deployments("fission").UpdateStatus(t.Context(), d, metav1.UpdateOptions{})
			require.NoError(t, err)
		}
		update(dep("router", 2, 2, 1))
		time.Sleep(11 * time.Second) // ≥2 polls observe the convergence
		update(dep("router", 2, 2, 0))
		time.Sleep(11 * time.Second) // ≥2 polls observe the regression
		update(dep("router", 2, 2, 1))
		time.Sleep(11 * time.Second)     // ≥2 polls observe the re-convergence
		update(dep("executor", 1, 1, 1)) // release the poll

		synctest.Wait()
		require.NoError(t, <-done)

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 1, events["router/rolled"], "rolled fires once despite repeated polls")
		assert.Equal(t, 2, events["router/converged"], "converged must fire again after a regression, not latch")
		assert.Equal(t, 1, events["router/regressed"], "a converged Deployment losing availability must be recorded")
		assert.Equal(t, 1, events["executor/rolled"], "a Deployment absent from the snapshot counts as rolled (new)")
		assert.Equal(t, 1, events["executor/converged"])
		assert.Zero(t, events["webhook/converged"],
			"a Deployment helm never changed must not dilute the rollout sequence")
		assert.Zero(t, events["webhook/rolled"])
	})
}

// readAttribution unmarshals the sidecar artifact writeArtifact produced.
func readAttribution(t *testing.T, dir string) attribution {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "upgrade", "upgrade-attribution.json"))
	require.NoError(t, err)
	var att attribution
	require.NoError(t, json.Unmarshal(data, &att))
	return att
}

// TestFailureRecordLatencyMillis guards the latency conversion the failure
// records use.
func TestFailureRecordLatencyMillis(t *testing.T) {
	t.Parallel()
	rec := newUpgradeRecorder(time.Now())
	rec.observe("poolmgr", loadgen.Observation{Status: 0, Err: fmt.Errorf("x"), Latency: 1500 * time.Millisecond})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.failures, 1)
	assert.InDelta(t, 1500.0, rec.failures[0].LatencyMS, 0.1)
}
