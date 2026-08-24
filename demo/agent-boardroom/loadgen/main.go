// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Command loadgen drives the agent-boardroom demo (see ../README.md and
// fission/distributed-agent-runtime/06-demo-plan.md): it creates a bounded
// number of session-scoped conversations against the fission-bundle
// --agentPort dispatcher (pkg/agentruntime/dispatcher.go), each sending a
// fixed number of turns separated by an idle "think" pause (the "90%-idle"
// profile the demo's density claim depends on), then reports the numbers
// 06-demo-plan.md calls out: sessions:pods density (via GET /registry/pool),
// p50/p95 turn latency, and the spread of sessions across serving pods (via
// GET /registry/agents/<ns>/<agent>/sessions).
//
// Deliberately stdlib-only: it lives in the main module but adds no new
// go.mod dependency, so it stays trivially runnable with a bare `go run`.
package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// sessionHeader/yieldHeader mirror pkg/agentruntime/dispatcher.go's
// HeaderSession/HeaderYield constants — duplicated here (not imported) so
// this stays a standalone stdlib-only binary.
const (
	sessionHeader = "X-Fission-Session"
	yieldHeader   = "X-Fission-Agent-Yield"
)

// config holds the parsed command-line flags.
type config struct {
	agentsURL   string
	namespace   string
	agent       string
	environment string
	sessions    int
	turns       int
	thinkMin    time.Duration
	thinkMax    time.Duration
	burst       bool
	token       string
	concurrency int
	timeout     time.Duration
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen: "+err.Error())
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: cfg.timeout}

	thinkNote := fmt.Sprintf("think %s-%s", cfg.thinkMin, cfg.thinkMax)
	if cfg.burst {
		thinkNote = "burst (no think)"
	}
	fmt.Printf("loadgen: %d sessions x %d turns -> POST %s/agents/%s/%s (%s, concurrency %d)\n",
		cfg.sessions, cfg.turns, cfg.agentsURL, cfg.namespace, cfg.agent, thinkNote, cfg.concurrency)

	results := runLoad(ctx, cfg, client)
	printReport(ctx, cfg, client, results)
}

// turnResult is one turn's outcome, fed into the report's latency/status
// aggregation.
type turnResult struct {
	latency time.Duration
	status  int
	yield   string
	err     error
}

// loadResults is everything runLoad collected: every turnResult plus the
// wall-clock time the whole run took.
type loadResults struct {
	turns     []turnResult
	wallClock time.Duration
}

// runLoad fires cfg.sessions independent session goroutines (cheap — they
// mostly sleep) that each run cfg.turns turns through a shared,
// cfg.concurrency-bounded semaphore. The semaphore is held only for the
// duration of one in-flight HTTP call, never across a session's "think"
// sleep — holding it across a 20-60s sleep would serialize the whole run
// into batches of cfg.concurrency sessions, several minutes long, defeating
// the "create 100 sessions cheaply" demo beat.
func runLoad(ctx context.Context, cfg config, client *http.Client) loadResults {
	sem := make(chan struct{}, cfg.concurrency)
	results := make(chan turnResult, cfg.sessions*cfg.turns)

	var wg sync.WaitGroup
	start := time.Now()
	for i := range cfg.sessions {
		sessionID := fmt.Sprintf("s-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runSession(ctx, cfg, client, sessionID, sem, results)
		}()
	}
	wg.Wait()
	close(results)

	res := loadResults{wallClock: time.Since(start)}
	for r := range results {
		res.turns = append(res.turns, r)
	}
	return res
}

// runSession drives one session's turn chain: acquire the semaphore, send a
// turn, release it, then (unless --burst) sleep a random think duration in
// [cfg.thinkMin, cfg.thinkMax] before the next turn. It stops early if ctx is
// cancelled (Ctrl-C) or a turn fails outright (a transport error, not just a
// non-2xx status — the dispatcher itself answers with a status code on most
// failure modes, so a transport error usually means the target is
// unreachable and further turns would just pile up more of the same).
func runSession(ctx context.Context, cfg config, client *http.Client, sessionID string, sem chan struct{}, results chan<- turnResult) {
	target := cfg.agentsURL + "/agents/" + cfg.namespace + "/" + cfg.agent
	for turn := range cfg.turns {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		res := sendTurn(ctx, client, target, sessionID, cfg.token, turn)
		<-sem
		results <- res
		if res.err != nil {
			return
		}

		if turn == cfg.turns-1 || cfg.burst {
			continue
		}
		think := cfg.thinkMin
		if span := cfg.thinkMax - cfg.thinkMin; span > 0 {
			think += time.Duration(rand.Int64N(int64(span) + 1))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(think):
		}
	}
}

// sendTurn issues one POST turn and reads the full response body BEFORE
// stamping latency: that is the end-to-end turn time a real caller would
// see, not just time-to-first-byte. The body is drained and closed either
// way so the underlying connection is eligible for reuse.
func sendTurn(ctx context.Context, client *http.Client, target, sessionID, token string, turn int) turnResult {
	payload, err := json.Marshal(map[string]string{
		"message": fmt.Sprintf("turn %d from %s", turn+1, sessionID),
	})
	if err != nil {
		return turnResult{err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return turnResult{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(sessionHeader, sessionID)
	setAuth(req, token)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return turnResult{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	latency := time.Since(start)

	return turnResult{latency: latency, status: resp.StatusCode, yield: resp.Header.Get(yieldHeader)}
}

func setAuth(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// podSummary/poolResponse and sessionStats/sessionRecord/sessionsResponse
// are minimal local mirrors of pkg/agentruntime's registry_api.go/pool.go
// wire shapes (RegistryAPI.ListSessions, PoolAPI.ServePool) — duplicated
// rather than imported so this binary pulls in none of that package's
// controller-runtime/client-go transitive dependencies.
type podSummary struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Ready       bool   `json:"ready"`
	Served      bool   `json:"served"`
	Environment string `json:"environment,omitempty"`
}

type poolResponse struct {
	Pods []podSummary `json:"pods"`
}

type sessionStats struct {
	Turns int64 `json:"turns"`
}

type sessionRecord struct {
	ID         string       `json:"id"`
	Status     string       `json:"status"`
	CurrentPod string       `json:"currentPod,omitempty"`
	Stats      sessionStats `json:"stats"`
}

type sessionsResponse struct {
	Sessions []sessionRecord `json:"sessions"`
	Next     string          `json:"next,omitempty"`
}

// fetchPool calls GET /registry/pool and returns the pods scoped to
// cfg.environment (both fixture functions share one Environment — see
// ../specs/env-node.yaml — so filtering by it excludes any unrelated pods
// the replica's cache also happens to see).
func fetchPool(ctx context.Context, cfg config, client *http.Client) ([]podSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.agentsURL+"/registry/pool", nil)
	if err != nil {
		return nil, err
	}
	setAuth(req, cfg.token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, errors.New("pool introspection unavailable (503 — cache warming or degraded RBAC)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /registry/pool: unexpected status %d", resp.StatusCode)
	}

	var out poolResponse
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("decoding /registry/pool response: %w", err)
	}

	pods := make([]podSummary, 0, len(out.Pods))
	for _, p := range out.Pods {
		if cfg.environment == "" || p.Environment == cfg.environment {
			pods = append(pods, p)
		}
	}
	return pods, nil
}

// fetchSessions pages through GET /registry/agents/<ns>/<agent>/sessions to
// completion, returning every session record the run left behind.
func fetchSessions(ctx context.Context, cfg config, client *http.Client) ([]sessionRecord, error) {
	const pageLimit = 500

	var all []sessionRecord
	page := ""
	for {
		u := fmt.Sprintf("%s/registry/agents/%s/%s/sessions?limit=%d",
			cfg.agentsURL, url.PathEscape(cfg.namespace), url.PathEscape(cfg.agent), pageLimit)
		if page != "" {
			u += "&page=" + url.QueryEscape(page)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		setAuth(req, cfg.token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		var out sessionsResponse
		decodeErr := json.UnmarshalRead(resp.Body, &out)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET /registry/agents/%s/%s/sessions: unexpected status %d", cfg.namespace, cfg.agent, resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decoding sessions response: %w", decodeErr)
		}

		all = append(all, out.Sessions...)
		if out.Next == "" {
			return all, nil
		}
		page = out.Next
	}
}

// printReport prints the turn-latency stats from the run itself, then
// queries the registry (GET /registry/pool and .../sessions) for the
// pod-density and pod-spread numbers that can only be observed after the
// fact.
func printReport(ctx context.Context, cfg config, client *http.Client, res loadResults) {
	var latencies []time.Duration
	statusCounts := map[int]int{}
	var transportErrs int
	for _, r := range res.turns {
		if r.err != nil {
			transportErrs++
			continue
		}
		latencies = append(latencies, r.latency)
		statusCounts[r.status]++
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	fmt.Println()
	fmt.Println("== turn results ==")
	fmt.Printf("wall clock:        %s\n", res.wallClock.Round(time.Millisecond))
	fmt.Printf("turns sent:        %d\n", len(res.turns))
	fmt.Printf("turns ok:          %d\n", len(latencies))
	fmt.Printf("transport errors:  %d\n", transportErrs)
	for _, code := range sortedStatusCodes(statusCounts) {
		fmt.Printf("  status %d:       %d\n", code, statusCounts[code])
	}
	if len(latencies) > 0 {
		fmt.Printf("p50 turn latency:  %s\n", percentile(latencies, 0.50).Round(time.Millisecond))
		fmt.Printf("p95 turn latency:  %s\n", percentile(latencies, 0.95).Round(time.Millisecond))
	}

	fmt.Println()
	fmt.Println("== registry snapshot (06-demo-plan.md success metrics) ==")
	pods, err := fetchPool(ctx, cfg, client)
	if err != nil {
		fmt.Printf("pods (env=%s):     unavailable (%s)\n", cfg.environment, err)
	} else {
		fmt.Printf("pods (env=%s):     %d\n", cfg.environment, len(pods))
	}

	sessions, err := fetchSessions(ctx, cfg, client)
	if err != nil {
		fmt.Printf("sessions:          unavailable (%s)\n", err)
		return
	}
	fmt.Printf("sessions:          %d\n", len(sessions))

	if err == nil && len(pods) > 0 {
		fmt.Printf("density (sessions:pods): %.1fx\n", float64(len(sessions))/float64(len(pods)))
	}

	distinctPods := map[string]struct{}{}
	for _, s := range sessions {
		if s.CurrentPod != "" {
			distinctPods[s.CurrentPod] = struct{}{}
		}
	}
	// This is a spread count from one END-OF-RUN snapshot, not a per-session
	// resume-on-different-pod tally (that needs a GET .../sessions/{id} after
	// every turn to see each session's own CurrentPod history) — labeled
	// accordingly so it isn't mistaken for the latter.
	fmt.Printf("distinct serving pods (final snapshot): %d\n", len(distinctPods))
}

func sortedStatusCodes(counts map[int]int) []int {
	codes := make([]int, 0, len(counts))
	for c := range counts {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	return codes
}

// percentile returns the p-th percentile (0 <= p <= 1) of an already-sorted
// slice via nearest-rank; sorted must be non-empty.
func percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

// parseFlags parses argv into a config, applying the documented defaults and
// validating the --think range.
func parseFlags(argv []string) (config, error) {
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	agentsURL := fs.String("agents-url", "http://127.0.0.1:8894", "base URL of the fission-bundle --agentPort service")
	namespace := fs.String("namespace", "default", "namespace the agent function is deployed in")
	agent := fs.String("agent", "support-desk", "agent function name")
	environment := fs.String("environment", "node", "Fission Environment name to scope the pool-density count to (empty = every pod the replica sees)")
	sessions := fs.Int("sessions", 100, "number of concurrent sessions to create")
	turns := fs.Int("turns", 3, "number of turns per session")
	think := fs.String("think", "20s-60s", `idle "think" time between turns, as "min-max" (e.g. "20s-60s") or a single fixed duration`)
	burst := fs.Bool("burst", false, "skip the think pause entirely; fire every session's turns back to back (contention/burst demo beats)")
	token := fs.String("token", "", "bearer token for a JWT_SIGNING_KEY-authenticated agent runtime (omit when AGENT_ALLOW_INSECURE=true)")
	concurrency := fs.Int("concurrency", 20, "maximum number of in-flight turn requests at once")
	timeout := fs.Duration("timeout", 90*time.Second, "per-request HTTP client timeout (generous: cold-start specialization is part of a turn)")
	if err := fs.Parse(argv); err != nil {
		return config{}, err
	}

	if *sessions <= 0 {
		return config{}, errors.New("--sessions must be > 0")
	}
	if *turns <= 0 {
		return config{}, errors.New("--turns must be > 0")
	}
	if *concurrency <= 0 {
		return config{}, errors.New("--concurrency must be > 0")
	}

	thinkMin, thinkMax, err := parseThinkRange(*think)
	if err != nil {
		return config{}, err
	}

	return config{
		agentsURL:   strings.TrimSuffix(*agentsURL, "/"),
		namespace:   *namespace,
		agent:       *agent,
		environment: *environment,
		sessions:    *sessions,
		turns:       *turns,
		thinkMin:    thinkMin,
		thinkMax:    thinkMax,
		burst:       *burst,
		token:       *token,
		concurrency: *concurrency,
		timeout:     *timeout,
	}, nil
}

// parseThinkRange parses "min-max" (e.g. "20s-60s") into two durations, or a
// single duration (e.g. "30s") used as both. It swaps an inverted range
// rather than erroring, since "60s-20s" unambiguously means the same span.
func parseThinkRange(s string) (time.Duration, time.Duration, error) {
	before, after, ok := strings.Cut(s, "-")
	if !ok {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, 0, fmt.Errorf("--think: %w", err)
		}
		return d, d, nil
	}

	minD, err := time.ParseDuration(before)
	if err != nil {
		return 0, 0, fmt.Errorf("--think: parsing min %q: %w", before, err)
	}
	maxD, err := time.ParseDuration(after)
	if err != nil {
		return 0, 0, fmt.Errorf("--think: parsing max %q: %w", after, err)
	}
	if minD > maxD {
		minD, maxD = maxD, minD
	}
	return minD, maxD, nil
}
