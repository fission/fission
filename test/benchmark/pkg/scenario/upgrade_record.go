// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// Outcome buckets for upgrade-under-load request classification. The recorder
// and the gate counters MUST agree on these, so classification lives in one
// function (classifyOutcome) that both consume — a divergence would let the
// timeline tell a different story from the numbers the error budget gates on.
const (
	bucketOK           = "ok"
	bucketTransport    = "transport"
	bucketAttributed   = "platform_attributed"
	bucketUnattributed = "unattributed"
)

// classifyOutcome maps a request observation to its budget bucket.
//
//   - status 0 (no response at all: dial error, reset, client timeout) →
//     transport — the strictest bucket; the router never answered.
//   - 2xx → ok.
//   - non-2xx carrying the RFC-0015 attribution header → platform_attributed.
//     ANY component value counts, including `function` and 499
//     client_disconnect: the header proves the router answered and classified,
//     and the budget is zero across every failure bucket anyway.
//   - non-2xx without the header → unattributed (e.g. a 404 from a
//     rebuilt-but-empty route table).
func classifyOutcome(o loadgen.Observation) string {
	switch {
	case o.Status == 0:
		return bucketTransport
	case o.Err == nil:
		return bucketOK
	case o.HeaderVal != "":
		return bucketAttributed
	default:
		return bucketUnattributed
	}
}

const (
	// timelineBucket is the resolution of the per-target outcome timeline.
	timelineBucket = 5 * time.Second
	// maxTimelineBuckets bounds the timeline against a clock gone wrong; the
	// real ceiling is baseline (30s) + upgrade budget (10m) + settle (2m)
	// ≈ 160 buckets.
	maxTimelineBuckets = 4096
	// maxFailureRecords bounds detailed per-failure records across ALL
	// targets (sized for this scenario's two; one noisy target may consume
	// it). The bucket totals keep counting past the cap, so nothing is lost
	// from the numbers — only from the per-failure detail.
	maxFailureRecords = 1024
	// maxEventRecords bounds pod/deployment lifecycle events.
	maxEventRecords = 4096
	// maxErrString keeps recorded error strings shorter than a stack trace.
	maxErrString = 200
)

// failureRecord is one non-ok request, timestamped relative to load start.
type failureRecord struct {
	OffsetS   float64 `json:"offsetS"`
	Target    string  `json:"target"`
	Bucket    string  `json:"bucket"`
	Status    int     `json:"status,omitempty"`
	Component string  `json:"component,omitempty"`
	Err       string  `json:"err,omitempty"`
	LatencyMS float64 `json:"latencyMs"`
}

// phaseMark is a named point in the scenario choreography.
type phaseMark struct {
	Name    string    `json:"name"`
	OffsetS float64   `json:"offsetS"`
	At      time.Time `json:"at"`
}

// lifecycleEvent is one pod or Deployment transition observed during the run.
type lifecycleEvent struct {
	OffsetS float64 `json:"offsetS"`
	Kind    string  `json:"kind"` // "pod" | "deployment"
	Name    string  `json:"name"` // namespace/name for pods, name for Deployments
	Event   string  `json:"event"`
	Detail  string  `json:"detail,omitempty"`
}

// bucketCounts is one timeline cell: request outcomes in one 5s window.
type bucketCounts struct {
	OK           int64 `json:"ok"`
	Transport    int64 `json:"transport"`
	Attributed   int64 `json:"attributed"`
	Unattributed int64 `json:"unattributed"`
}

func (b *bucketCounts) add(bucket string) {
	switch bucket {
	case bucketOK:
		b.OK++
	case bucketTransport:
		b.Transport++
	case bucketAttributed:
		b.Attributed++
	default:
		b.Unattributed++
	}
}

func (b *bucketCounts) failures() int64 { return b.Transport + b.Attributed + b.Unattributed }

// upgradeRecorder turns the upgrade scenario's request stream and cluster
// events into an attributable timeline. Everything is bounded and best-effort:
// the recorder must never be the reason the gate errors.
type upgradeRecorder struct {
	t0 time.Time

	mu       sync.Mutex
	failures []failureRecord
	dropped  map[string]int64
	timeline map[string][]bucketCounts
	phases   []phaseMark
	events   []lifecycleEvent
}

func newUpgradeRecorder(t0 time.Time) *upgradeRecorder {
	return &upgradeRecorder{
		t0:       t0,
		dropped:  map[string]int64{},
		timeline: map[string][]bucketCounts{},
	}
}

func (r *upgradeRecorder) offset(at time.Time) float64 { return at.Sub(r.t0).Seconds() }

// observe records one request outcome into the timeline, and — when it is a
// failure — into the bounded per-failure detail.
func (r *upgradeRecorder) observe(target string, o loadgen.Observation) {
	bucket := classifyOutcome(o)
	now := time.Now()
	idx := int(now.Sub(r.t0) / timelineBucket)
	if idx < 0 || idx >= maxTimelineBuckets {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	tl := r.timeline[target]
	for len(tl) <= idx {
		tl = append(tl, bucketCounts{})
	}
	tl[idx].add(bucket)
	r.timeline[target] = tl

	if bucket == bucketOK {
		return
	}
	if len(r.failures) >= maxFailureRecords {
		r.dropped[target]++
		return
	}
	errStr := ""
	if o.Err != nil {
		errStr = o.Err.Error()
		if len(errStr) > maxErrString {
			errStr = errStr[:maxErrString]
		}
	}
	r.failures = append(r.failures, failureRecord{
		OffsetS:   r.offset(now),
		Target:    target,
		Bucket:    bucket,
		Status:    o.Status,
		Component: o.HeaderVal,
		Err:       errStr,
		LatencyMS: millis(o.Latency),
	})
}

// mark records a named choreography point (baseline end, upgrade start, …).
func (r *upgradeRecorder) mark(name string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phases = append(r.phases, phaseMark{Name: name, OffsetS: r.offset(now), At: now})
}

// deployEvent records one Deployment rollout transition (from the rollout
// convergence poll).
func (r *upgradeRecorder) deployEvent(name, event string) {
	r.event("deployment", name, event, "")
}

// metaInto stamps the phase markers (and record-drop accounting) into the
// scenario result's Meta, which survives report.Aggregate at Repetitions=1.
// The rollout_sequence value is the compact "which Deployment converged when"
// answer, readable without downloading the artifact.
func (r *upgradeRecorder) metaInto(res *report.ScenarioResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.phases {
		key := "phase_" + strings.ReplaceAll(p.Name, "-", "_")
		res.SetMeta(key, p.At.Format(time.RFC3339Nano))
		res.SetMeta(key+"_offset_s", fmt.Sprintf("%.1f", p.OffsetS))
	}
	// LAST converged per Deployment: a converge->regress->re-converge cycle
	// must report the settle time that stuck, not the first false one. Order
	// by final convergence.
	lastConverged := map[string]float64{}
	for _, e := range r.events {
		if e.Kind != "deployment" {
			continue
		}
		switch e.Event {
		case "converged":
			lastConverged[e.Name] = e.OffsetS
		case "regressed":
			delete(lastConverged, e.Name)
		}
	}
	type depAt struct {
		name string
		at   float64
	}
	orderedDeps := make([]depAt, 0, len(lastConverged))
	for name, at := range lastConverged {
		orderedDeps = append(orderedDeps, depAt{name, at})
	}
	sort.Slice(orderedDeps, func(i, j int) bool { return orderedDeps[i].at < orderedDeps[j].at })
	seq := make([]string, 0, len(orderedDeps))
	for _, d := range orderedDeps {
		seq = append(seq, fmt.Sprintf("%s@%.0fs", d.name, d.at))
	}
	if len(seq) > 0 {
		res.SetMeta("rollout_sequence", strings.Join(seq, ","))
	}
	// Two separate drop meters, deliberately: on a red run,
	// "failure_records_dropped" reads as "N failures were not captured", and
	// folding lifecycle-event overflow into it would claim the opposite of
	// the truth.
	var failDropped int64
	for k, n := range r.dropped {
		if k != "events" {
			failDropped += n
		}
	}
	if failDropped > 0 {
		res.SetMeta("failure_records_dropped", fmt.Sprintf("%d", failDropped))
	}
	if n := r.dropped["events"]; n > 0 {
		res.SetMeta("lifecycle_events_dropped", fmt.Sprintf("%d", n))
	}
}

// event records a pod/Deployment lifecycle transition (bounded).
func (r *upgradeRecorder) event(kind, name, event, detail string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) >= maxEventRecords {
		r.dropped["events"]++
		return
	}
	r.events = append(r.events, lifecycleEvent{
		OffsetS: r.offset(now), Kind: kind, Name: name, Event: event, Detail: detail,
	})
}

// attribution is the sidecar artifact schema (upgrade-attribution.json).
type attribution struct {
	T0       time.Time                 `json:"t0"`
	Phases   []phaseMark               `json:"phases"`
	Events   []lifecycleEvent          `json:"events"`
	Failures []failureRecord           `json:"failures"`
	Timeline map[string][]bucketCounts `json:"timeline"`
	Dropped  map[string]int64          `json:"dropped,omitempty"`
}

// writeArtifact writes the attribution JSON under dir (no-op when dir is "").
// Best-effort: an unwritable artifact must not fail the scenario.
func (r *upgradeRecorder) writeArtifact(dir string) {
	if dir == "" {
		return
	}
	// Marshal under the lock. The pod watchers are joined before the output
	// block, but that join is a call-site convention two files away — and
	// marshalling the timeline and dropped MAPS outside the lock against one
	// straggling `rec.event` is a fatal concurrent-map throw, the one way
	// this best-effort recorder could kill the run it exists to explain.
	// This runs once at scenario end; contention is irrelevant.
	r.mu.Lock()
	data, err := json.MarshalIndent(attribution{
		T0: r.t0, Phases: r.phases, Events: r.events,
		Failures: r.failures, Timeline: r.timeline, Dropped: r.dropped,
	}, "", "  ")
	r.mu.Unlock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "upgrade recorder: marshal attribution: %v\n", err)
		return
	}
	target := filepath.Join(dir, "upgrade")
	if err := os.MkdirAll(target, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "upgrade recorder: mkdir artifact dir: %v\n", err)
		return
	}
	if err := os.WriteFile(filepath.Join(target, "upgrade-attribution.json"), data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "upgrade recorder: write attribution: %v\n", err)
	}
}

// logSummary prints the human-readable timeline: per-5s outcome buckets for
// both targets with phase markers and control-plane pod transitions
// interleaved, so a red run is diagnosable from the job log alone.
func (r *upgradeRecorder) logSummary(w io.Writer, targets []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Interleavable markers: phases plus pod/Deployment events, sorted by
	// offset. Pod events are already filtered to the interesting set by the
	// watcher; Deployment events come from the rollout poll.
	type marker struct {
		offsetS float64
		text    string
	}
	var marks []marker
	for _, p := range r.phases {
		marks = append(marks, marker{p.OffsetS, fmt.Sprintf("[%s]", p.Name)})
	}
	for _, e := range r.events {
		text := fmt.Sprintf("[%s %s %s", e.Kind, e.Name, e.Event)
		if e.Detail != "" {
			text += " " + e.Detail
		}
		marks = append(marks, marker{e.OffsetS, text + "]"})
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i].offsetS < marks[j].offsetS })

	nBuckets := 0
	for _, tl := range r.timeline {
		nBuckets = max(nBuckets, len(tl))
	}

	fmt.Fprintf(w, "=== upgrade-under-load timeline (%s buckets; ok/transport/attributed/unattributed) ===\n", timelineBucket)
	mi := 0
	flaggedFirstFailure := false
	for idx := range nBuckets {
		lo := float64(idx) * timelineBucket.Seconds()
		hi := lo + timelineBucket.Seconds()
		for mi < len(marks) && marks[mi].offsetS < hi {
			fmt.Fprintf(w, "%7.1fs %s\n", marks[mi].offsetS, marks[mi].text)
			mi++
		}
		var line strings.Builder
		fmt.Fprintf(&line, "%4.0f-%.0fs ", lo, hi)
		failing := false
		for _, target := range targets {
			var c bucketCounts
			if tl := r.timeline[target]; idx < len(tl) {
				c = tl[idx]
			}
			fmt.Fprintf(&line, " %s %d/%d/%d/%d", target, c.OK, c.Transport, c.Attributed, c.Unattributed)
			if c.failures() > 0 {
				failing = true
			}
		}
		if failing && !flaggedFirstFailure {
			flaggedFirstFailure = true
			line.WriteString("   <- first failing bucket")
		}
		fmt.Fprintln(w, line.String())
	}
	for ; mi < len(marks); mi++ {
		fmt.Fprintf(w, "%7.1fs %s\n", marks[mi].offsetS, marks[mi].text)
	}

	if len(r.failures) > 0 {
		const show = 20
		fmt.Fprintf(w, "=== failures (first %d of %d captured; full detail in upgrade-attribution.json) ===\n", min(show, len(r.failures)), len(r.failures))
		for i, f := range r.failures {
			if i >= show {
				break
			}
			fmt.Fprintf(w, "%7.1fs %-9s %-19s status=%d comp=%q lat=%.0fms err=%q\n",
				f.OffsetS, f.Target, f.Bucket, f.Status, f.Component, f.LatencyMS, f.Err)
		}
	}
	for k, n := range r.dropped {
		fmt.Fprintf(w, "note: %d %s records dropped past the cap (totals remain complete)\n", n, k)
	}
}
