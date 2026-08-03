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
			c.observe(tc.o)
			counts := map[string]int64{
				bucketOK:           c.ok.Load(),
				bucketTransport:    c.transport.Load(),
				bucketAttributed:   c.attributed.Load(),
				bucketUnattributed: c.otherErr.Load(),
			}
			for bucket, n := range counts {
				want := int64(0)
				if bucket == tc.bucket {
					want = 1
				}
				assert.Equalf(t, want, n, "counter %s after a %s observation", bucket, tc.bucket)
			}
			assert.Equal(t, int64(1), c.total.Load())
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

	rec.mu.Lock()
	tl := rec.timeline["poolmgr"]
	require.GreaterOrEqual(t, len(tl), 3, "observations 12s after t0 must land in bucket index 2")
	assert.Equal(t, int64(1), tl[2].OK)
	assert.Equal(t, int64(1), tl[2].Attributed)
	assert.Len(t, rec.failures, 1, "only the failure gets a detail record")
	rec.mu.Unlock()

	// Cap: flood past maxFailureRecords*2 and require the overflow to be
	// counted, not silently discarded — and the TIMELINE to keep counting.
	var wg sync.WaitGroup
	const goroutines = 8
	const perGoroutine = (maxFailureRecords*2 + 100) / goroutines
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

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Len(t, rec.failures, maxFailureRecords*2, "detail records stop at the cap")
	assert.Positive(t, rec.dropped["newdeploy"], "overflow must be accounted, not silent")
	var timelineTotal int64
	for _, b := range rec.timeline["newdeploy"] {
		timelineTotal += b.Attributed
	}
	assert.Equal(t, int64(flood), timelineTotal,
		"the timeline must count every observation even after detail records cap out")
}

// TestRecorderSummaryAndArtifact renders the human summary and the sidecar
// artifact from one populated recorder and checks the load-bearing content:
// phase markers interleaved, the first failing bucket flagged, failures listed,
// and the artifact JSON round-tripping with the same counts.
func TestRecorderSummaryAndArtifact(t *testing.T) {
	t.Parallel()

	rec := newUpgradeRecorder(time.Now().Add(-40 * time.Second))
	rec.phases = append(rec.phases,
		phaseMark{Name: "load-start", OffsetS: 0},
		phaseMark{Name: "upgrade-cmd-start", OffsetS: 30.2},
	)
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
	assert.Contains(t, out, "transport", "the failure detail lines must name the bucket")
	assert.Contains(t, out, "connection reset", "the failure detail lines must carry the error")

	dir := t.TempDir()
	rec.writeArtifact(dir)
	data, err := os.ReadFile(filepath.Join(dir, "upgrade", "upgrade-attribution.json"))
	require.NoError(t, err)
	var att attribution
	require.NoError(t, json.Unmarshal(data, &att))
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
// synctest bubble so the 5s poll interval costs nothing, and asserts the
// per-Deployment rolled/converged events fire exactly once each.
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
		// is not yet converged; executor is new (absent from the snapshot).
		kube := fake.NewClientset(
			dep("router", 2, 1, 0),
			dep("executor", 1, 1, 1),
		)
		gensBefore := map[string]int64{"router": 1}

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

		// Let several polls observe the unconverged router, then converge it.
		time.Sleep(12 * time.Second)
		converged := dep("router", 2, 2, 1)
		_, err := kube.AppsV1().Deployments("fission").UpdateStatus(context.WithoutCancel(t.Context()), converged, metav1.UpdateOptions{})
		require.NoError(t, err)

		synctest.Wait()
		require.NoError(t, <-done)

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 1, events["router/rolled"], "rolled fires once despite repeated polls")
		assert.Equal(t, 1, events["router/converged"])
		assert.Equal(t, 1, events["executor/rolled"], "a Deployment absent from the snapshot counts as rolled (new)")
		assert.Equal(t, 1, events["executor/converged"])
	})
}

// millisSanity guards the latency conversion the failure records use.
func TestFailureRecordLatencyMillis(t *testing.T) {
	t.Parallel()
	rec := newUpgradeRecorder(time.Now())
	rec.observe("poolmgr", loadgen.Observation{Status: 0, Err: fmt.Errorf("x"), Latency: 1500 * time.Millisecond})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.failures, 1)
	assert.InDelta(t, 1500.0, rec.failures[0].LatencyMS, 0.1)
}
