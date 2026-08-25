// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/agentruntime"
	"github.com/fission/fission/test/integration/framework"
)

// turnAttemptTimeout bounds a single POST /agents/... attempt.
// f.HTTPClient() sets no client-wide timeout (per its own doc: dials block
// until the backend accepts), so an unbounded per-request context would let
// one hung attempt (a stuck dial, a pathological cold start) consume the
// entire require.EventuallyWithT budget below as a single try, turning
// "retry until it converges" into "one shot, then fail". 60s leaves ~3 real
// attempts inside the 180s polling budget.
const turnAttemptTimeout = 60 * time.Second

// registryAttemptTimeout bounds a single GET /registry/... attempt inside
// the registry EventuallyWithT loops below. Unlike a turn (which proxies
// into a function and can legitimately take a while), a registry GET is a
// cheap in-memory read off this replica's AgentView/session store — it
// never needs turnAttemptTimeout's 60s allowance. A short per-attempt
// timeout inside the same 60s overall polling budget instead buys several
// real retries, so a single hung dial doesn't burn the whole budget as one
// try.
const registryAttemptTimeout = 10 * time.Second

// TestAgentRuntimeSessionDispatch exercises the agent-runtime session
// dispatch path end-to-end against a real Node.js runtime: a function
// created with `--agent` (default header session source, X-Fission-Session)
// is (1) reachable at POST /agents/<ns>/<fn>, (2) mints a session id on a
// turn with no session header and returns it via X-Fission-Session, and (3)
// echoes the SAME id back when the caller supplies it on a follow-up turn.
// It also asserts the plain (non-yielding) fixture leaves
// X-Fission-Agent-Yield unset on the response, since the dispatcher only
// surfaces that header when the function itself sets it.
//
// The agent runtime is enabled in the kind/kind-ci skaffold profiles
// (agentRuntime.enabled/allowInsecure, mirroring mcp.enabled/allowInsecure);
// the framework reaches it through the agentruntime.fission route
// (in-process port-forward, or the FISSION_AGENTRUNTIME_BASE_URL override).
// The test skips when the endpoint is unreachable — same shape as
// TestMCPToolsListAndCall's requireMCPReachable. In CI the server runs in
// pass-through auth mode (AGENT_ALLOW_INSECURE=true), so no bearer token is
// needed here; JWT namespace scoping is covered by the pkg/agentruntime unit
// tests (authz_test.go).
func TestAgentRuntimeSessionDispatch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	requireAgentRuntimeReachable(t, ctx, f)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-agent-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	fnName := "agent-hello-" + ns.ID
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName,
		Env:  envName,
		Code: framework.WriteTestData(t, "nodejs/hello/hello.js"),
	})
	ns.WaitForFunction(t, ctx, fnName)

	// FunctionOptions has no Agent field yet (RFC agent-runtime slices 1-2
	// added the CLI flag but this framework helper predates it); attach the
	// Agent config with a follow-up `fn update --agent`. The bare flag (no
	// --agent-session-*/--agent-idle-after/etc.) leaves Session/IdleAfter/
	// ArchiveAfter/MaxSessions nil, which the agent runtime's AgentView
	// resolves to its documented defaults: header source
	// X-Fission-Session, 5m idle, 24h archive, unlimited sessions (see
	// pkg/agentruntime/view.go's entryFromAgentConfig).
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--agent")

	turnURL := f.AgentRuntimeBaseURL() + "/agents/" + ns.Name + "/" + fnName

	// First turn: no session header. The dispatcher mints one, and by the
	// time this returns 200 the agentruntime replica's reconciler has also
	// caught up with the Function admission — poll until 200 to absorb both
	// that convergence and the function's cold start.
	var sessionID string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postTurn(attemptCtx, f, turnURL, "")
		if !assert.NoErrorf(c, err, "POST %s", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "first turn to %s", turnURL) {
			return
		}
		sid := resp.Header.Get(agentruntime.HeaderSession)
		if !assert.NotEmpty(c, sid, "first turn must mint a session id via %s", agentruntime.HeaderSession) {
			return
		}
		assert.Empty(c, resp.Header.Get(agentruntime.HeaderYield),
			"a plain function that never sets %s must not surface it on the response", agentruntime.HeaderYield)
		sessionID = sid
	}, 180*time.Second, 2*time.Second)
	require.NotEmpty(t, sessionID, "first turn never minted a session id")

	// Negative path (Task 7 step 2): this function was created WITHOUT
	// State, so its AgentEntry.StateKeyspace is empty and GetHistory's
	// early-return branch (registry_api.go) must answer 200 with an empty
	// history — never touching the (nonexistent) statesvc-backed history
	// store, and never a 404/500. One extra GET on this test's already-
	// minted session is enough; it does not need its own function/skip
	// setup (TestAgentRuntimeHistory below covers the WITH-State positive
	// path against a separate function).
	historyURL := f.AgentRuntimeBaseURL() + "/registry/agents/" + ns.Name + "/" + fnName + "/sessions/" + sessionID + "/history"
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, registryAttemptTimeout)
		defer cancel()
		body, status, err := getRegistry(attemptCtx, f, historyURL)
		if !assert.NoErrorf(c, err, "GET %s", historyURL) {
			return
		}
		if !assert.Equalf(c, http.StatusOK, status, "GET %s", historyURL) {
			return
		}
		var payload historyResponseDTO
		if !assert.NoErrorf(c, json.Unmarshal(body, &payload), "decoding %s body %q", historyURL, body) {
			return
		}
		assert.Equalf(c, int64(0), payload.Head, "a no-State agent's session must report head:0, got %d", payload.Head)
		// assert.Empty alone would also pass for a nil slice (an "events":null
		// wire body decodes to nil, not []) — NotNil plus an explicit length
		// check is what actually pins the documented [] shape.
		assert.NotNilf(c, payload.Events, "a no-State agent's session must report events as [], not null")
		assert.Lenf(c, payload.Events, 0, "a no-State agent's session must report an empty events list, got %+v", payload.Events)
	}, 60*time.Second, 2*time.Second)

	// Second turn: supply the minted session id explicitly. The dispatcher
	// must resolve (not mint a new) session and echo the same id back.
	// Wrapped in the same EventuallyWithT/attempt-timeout shape as the first
	// turn (symmetric with TestMCPToolsListAndCall's tools/list + tools/call
	// subtests, both of which poll) rather than a single direct call.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postTurn(attemptCtx, f, turnURL, sessionID)
		if !assert.NoErrorf(c, err, "POST %s", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "second turn to %s", turnURL) {
			return
		}
		assert.Equal(c, sessionID, resp.Header.Get(agentruntime.HeaderSession),
			"second turn must echo the caller-supplied session id, not mint a new one")
		assert.Empty(c, resp.Header.Get(agentruntime.HeaderYield))
	}, 180*time.Second, 2*time.Second)
}

// TestAgentRuntimeYieldContinue exercises the Task 14/15 yield=continue
// self-continuation chain end-to-end: a session whose function replies
// X-Fission-Agent-Yield: continue is re-dispatched by WakeService without
// any further caller action. The fixture (nodejs/agentloop/loop.js) reads
// the dispatcher's own X-Fission-Agent-Turns header and echoes it back as
// {"turns": n}, continuing while n < 4 — so the test proves the chain
// advanced purely from the wake queue by polling with fresh "peek" turns on
// the SAME session id and watching the reported turn count climb, with no
// introspection API to read session state directly.
func TestAgentRuntimeYieldContinue(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	requireAgentRuntimeReachable(t, ctx, f)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-agentloop-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	fnName := "agent-loop-" + ns.ID
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName,
		Env:  envName,
		Code: framework.WriteTestData(t, "nodejs/agentloop/loop.js"),
	})
	ns.WaitForFunction(t, ctx, fnName)
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--agent")

	turnURL := f.AgentRuntimeBaseURL() + "/agents/" + ns.Name + "/" + fnName

	// First turn: no session header. The fixture sees turns=0 (< 4), so the
	// response must carry the continue yield header and the dispatcher must
	// have enqueued a wake to re-invoke this session on its own.
	var sessionID string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postTurn(attemptCtx, f, turnURL, "")
		if !assert.NoErrorf(c, err, "POST %s", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "first turn to %s", turnURL) {
			return
		}
		sid := resp.Header.Get(agentruntime.HeaderSession)
		if !assert.NotEmpty(c, sid, "first turn must mint a session id via %s", agentruntime.HeaderSession) {
			return
		}
		assert.Equal(c, agentruntime.YieldContinue, resp.Header.Get(agentruntime.HeaderYield),
			"first turn (turns=0 < 4) must ask for a continuation")
		sessionID = sid
	}, 180*time.Second, 2*time.Second)
	require.NotEmpty(t, sessionID, "first turn never minted a session id")

	// The wake service chains the session forward in the background (no
	// further action from this test). Poll with fresh "peek" turns on the
	// same session id until the fixture reports turns >= 5.
	//
	// Each peek is ITSELF a turn dispatch, so it also increments the
	// session's turn counter — a peek that merely observed the raw counter
	// reaching 5 would pass even with the wake service dead (5 peeks alone
	// get there). To actually prove background chaining, track P = the
	// number of turns THIS TEST has dispatched so far (the initial turn plus
	// every peek attempt, counting timeouts/failures too, since a timed-out
	// POST may still have landed server-side). A turn's reported count n is
	// the session's turn tally from BEFORE that turn's own dispatch, so at
	// most P-1 of it can be test-caused; the rest, W = n-(P-1), must be
	// wake-driven. Requiring n >= P+2 (W >= 3) alongside n >= 5 is what
	// actually falsifies on a dead wake service: e.g. peek 3 (P=4) would
	// need n >= 6, but a dead chain only ever reaches n=3 by then.
	peeks := 1 // the initial turn above already counts as one dispatch
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		peeks++
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postTurn(attemptCtx, f, turnURL, sessionID)
		if !assert.NoErrorf(c, err, "POST %s (peek)", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "peek turn to %s", turnURL) {
			return
		}
		body, err := io.ReadAll(resp.Body)
		if !assert.NoErrorf(c, err, "reading peek response body") {
			return
		}
		var payload struct {
			Turns int `json:"turns"`
		}
		if !assert.NoErrorf(c, json.Unmarshal(body, &payload), "decoding peek response body %q", body) {
			return
		}
		assert.GreaterOrEqualf(c, payload.Turns, 5,
			"session %s should have chained past turns=4 via wake-driven continuations by now (got %d)",
			sessionID, payload.Turns)
		assert.GreaterOrEqualf(c, payload.Turns, peeks+2,
			"session %s turns=%d after %d test-issued dispatches implies fewer than 3 wake-driven continuations; the wake service may not be chaining",
			sessionID, payload.Turns, peeks)
	}, 120*time.Second, 2*time.Second)
}

// TestAgentRuntimeRegistrySmoke exercises Task 18/20's read-only
// introspection surface end-to-end: after one real turn against a plain
// (non-yielding) agent function, the created function and its minted
// session must be visible through GET /registry/agents and GET
// /registry/agents/{ns}/{fn}/sessions, GET /registry/pool must answer with
// either a populated pod list or a documented degraded 503 (never anything
// else), and GET /registry/events must open an SSE stream whose first frame
// is an `event:` line (the connect-time snapshot replay — see events.go's
// ServeHTTP doc comment).
//
// Like the other two tests in this file, it skips when the agent runtime
// endpoint isn't reachable, and needs no bearer token: the kind/kind-ci
// skaffold profiles run the agent runtime with AGENT_ALLOW_INSECURE=true, and
// Authorizer.ScopeFromBearer/ScopeFromTokenInfo both mirror that pass-through
// stance when no signing key is configured (authz.go).
func TestAgentRuntimeRegistrySmoke(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	requireAgentRuntimeReachable(t, ctx, f)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-agent-registry-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	fnName := "agent-registry-" + ns.ID
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName,
		Env:  envName,
		Code: framework.WriteTestData(t, "nodejs/hello/hello.js"),
	})
	ns.WaitForFunction(t, ctx, fnName)
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--agent")

	base := f.AgentRuntimeBaseURL()
	turnURL := base + "/agents/" + ns.Name + "/" + fnName

	// One real turn, same shape as TestAgentRuntimeSessionDispatch's first
	// turn: mints a session id this test then looks for through the
	// registry API.
	var sessionID string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postTurn(attemptCtx, f, turnURL, "")
		if !assert.NoErrorf(c, err, "POST %s", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "turn to %s", turnURL) {
			return
		}
		sid := resp.Header.Get(agentruntime.HeaderSession)
		if !assert.NotEmpty(c, sid, "turn must mint a session id via %s", agentruntime.HeaderSession) {
			return
		}
		sessionID = sid
	}, 180*time.Second, 2*time.Second)
	require.NotEmpty(t, sessionID, "turn never minted a session id")

	// 1. GET /registry/agents: the created function must be listed by
	// namespace+name. Wrapped in EventuallyWithT since this replica's
	// AgentView reconciles the Function admission asynchronously.
	agentsURL := base + "/registry/agents"
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, registryAttemptTimeout)
		defer cancel()
		body, status, err := getRegistry(attemptCtx, f, agentsURL)
		if !assert.NoErrorf(c, err, "GET %s", agentsURL) {
			return
		}
		if !assert.Equalf(c, http.StatusOK, status, "GET %s", agentsURL) {
			return
		}
		var payload struct {
			Agents []struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"agents"`
		}
		if !assert.NoErrorf(c, json.Unmarshal(body, &payload), "decoding %s body %q", agentsURL, body) {
			return
		}
		found := false
		for _, a := range payload.Agents {
			if a.Namespace == ns.Name && a.Name == fnName {
				found = true
				break
			}
		}
		assert.Truef(c, found, "function %s/%s must appear in %s (got %+v)", ns.Name, fnName, agentsURL, payload.Agents)
	}, 60*time.Second, 2*time.Second)

	// 2. GET /registry/agents/{ns}/{fn}/sessions: the session minted above
	// must appear with at least one counted turn.
	sessionsURL := base + "/registry/agents/" + ns.Name + "/" + fnName + "/sessions"
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, registryAttemptTimeout)
		defer cancel()
		body, status, err := getRegistry(attemptCtx, f, sessionsURL)
		if !assert.NoErrorf(c, err, "GET %s", sessionsURL) {
			return
		}
		if !assert.Equalf(c, http.StatusOK, status, "GET %s", sessionsURL) {
			return
		}
		var payload struct {
			Sessions []struct {
				ID    string `json:"id"`
				Stats struct {
					Turns int64 `json:"turns"`
				} `json:"stats"`
			} `json:"sessions"`
		}
		if !assert.NoErrorf(c, json.Unmarshal(body, &payload), "decoding %s body %q", sessionsURL, body) {
			return
		}
		var found bool
		for _, s := range payload.Sessions {
			if s.ID == sessionID {
				found = true
				assert.GreaterOrEqualf(c, s.Stats.Turns, int64(1),
					"session %s must show at least one counted turn", sessionID)
				break
			}
		}
		assert.Truef(c, found, "session %s must appear in %s (got %d sessions)", sessionID, sessionsURL, len(payload.Sessions))
	}, 60*time.Second, 2*time.Second)

	// 3. GET /registry/pool: the kind/kind-ci profiles run poolmgr warm
	// pods, so a healthy replica should report at least one pod. A 503 is
	// also acceptable — the pool cache/RBAC preflight can still be warming
	// on a fresh cluster (pool.go's degraded/synced gates) — but is logged
	// rather than silently accepted, and any OTHER status fails the test.
	poolURL := base + "/registry/pool"
	body, status, err := getRegistry(ctx, f, poolURL)
	require.NoErrorf(t, err, "GET %s", poolURL)
	switch status {
	case http.StatusOK:
		var payload struct {
			Pods []json.RawMessage `json:"pods"`
		}
		require.NoErrorf(t, json.Unmarshal(body, &payload), "decoding %s body %q", poolURL, body)
		assert.GreaterOrEqualf(t, len(payload.Pods), 1,
			"expected at least one warm pool pod on the kind/kind-ci profile, got %s", body)
	case http.StatusServiceUnavailable:
		t.Logf("%s returned 503 (pool RBAC/cache still warming on this cluster): %s", poolURL, body)
	default:
		t.Fatalf("unexpected status from %s: %d body=%s", poolURL, status, body)
	}

	// 4. GET /registry/events: a plain GET (no EventSource — go's
	// net/http can set arbitrary headers, unlike a browser EventSource,
	// which is why the server also accepts ?token= for that client; see
	// events.go's bearerFromRequest doc). The connect-time snapshot replay
	// (ServeHTTP) writes any already-known session BEFORE the streaming
	// loop starts, so the very first line off the wire must be an
	// `event: ...` frame.
	eventsURL := base + "/registry/events"
	eventsCtx, eventsCancel := context.WithTimeout(ctx, 10*time.Second)
	defer eventsCancel()
	req, err := http.NewRequestWithContext(eventsCtx, http.MethodGet, eventsURL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := f.HTTPClient().Do(req)
	require.NoErrorf(t, err, "GET %s", eventsURL)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "GET %s", eventsURL)
	require.Equalf(t, "text/event-stream", resp.Header.Get("Content-Type"), "GET %s Content-Type", eventsURL)

	reader := bufio.NewReader(resp.Body)
	firstEvent, firstData, err := nextSSEFrame(reader)
	require.NoErrorf(t, err, "reading first SSE frame from %s", eventsURL)
	assert.NotEmptyf(t, firstEvent, "first SSE frame from %s must be an event: frame (connect-time snapshot), got event=%q data=%q", eventsURL, firstEvent, firstData)

	// Strengthen beyond "a stream of the right shape opened": prove the
	// connect-time snapshot actually replayed THIS test's session, which
	// falsifies a broken snapshot replay (a missing or corrupted record)
	// rather than just a dead endpoint. The FIRST frame is not assumed to be
	// it: CI runs the agent runtime with AGENT_ALLOW_INSECURE=true, which
	// gives every caller a wildcard scope, so the snapshot can also carry
	// sessions from the OTHER agent-runtime tests running in parallel in
	// this suite, and Poller.Snapshot's ordering is explicitly unspecified
	// (events.go). So this scans every remaining "session" frame in the
	// stream, bounded by eventsCtx's 10s deadline, for a record whose id
	// matches the sessionID minted earlier in this same test.
	foundSessionID := sseFrameMatchesSession(firstEvent, firstData, sessionID)
	for !foundSessionID {
		evt, data, ferr := nextSSEFrame(reader)
		if ferr != nil {
			break // eventsCtx's deadline was reached, or the stream ended
		}
		foundSessionID = sseFrameMatchesSession(evt, data, sessionID)
	}
	assert.Truef(t, foundSessionID,
		"no session frame from %s carried the session id %s minted earlier in this test; the snapshot replay may be broken",
		eventsURL, sessionID)
}

// historyEventDTO / historyResponseDTO / checkpointDTO decode the registry
// history/checkpoint routes' wire shape (pkg/agentruntime/registry_api.go's
// historyEvent/historyResponse, and pkg/agentruntime/history.go's
// CheckpointRecord). Payload is Go []byte in both: encoding/json
// base64-decodes a []byte field on Unmarshal automatically, the same
// encoding the server used to marshal it, so no manual base64 step is needed
// here beyond decoding the JSON envelope underneath.
type historyEventDTO struct {
	Seq     int64     `json:"seq"`
	Type    string    `json:"type"`
	Payload []byte    `json:"payload"`
	At      time.Time `json:"at"`
}

type historyResponseDTO struct {
	Head   int64             `json:"head"`
	Events []historyEventDTO `json:"events"`
}

type checkpointDTO struct {
	V                 int             `json:"v"`
	CoveredThroughSeq int64           `json:"coveredThroughSeq"`
	Kind              string          `json:"kind"`
	Payload           json.RawMessage `json:"payload"`
	CreatedAt         time.Time       `json:"createdAt"`
	Turn              int64           `json:"turn"`
}

// decodeEventTurn decodes e's payload (test/integration/testdata/nodejs/
// agenthistory/history.js's {v,turn,step,kind,at} envelope, mirroring
// demo/agent-boardroom/fixtures/support-desk.js's wire contract) and returns
// its turn field.
func decodeEventTurn(c *assert.CollectT, e historyEventDTO) (int, bool) {
	var p struct {
		Turn int `json:"turn"`
	}
	if !assert.NoErrorf(c, json.Unmarshal(e.Payload, &p), "decoding history event payload %s", e.Payload) {
		return 0, false
	}
	return p.Turn, true
}

// TestAgentRuntimeHistory exercises RFC-0027 G13's session history and
// checkpoint wire path end-to-end: a function created with `--agent`
// (applied via the same CLI-update workaround as the other tests in this
// file) and State: true (framework.FunctionOptions, the same pattern
// TestFunctionState uses) writes real per-turn history events and a
// mid-session checkpoint through statesvc's scoped EventLog/KV routes — see
// test/integration/testdata/nodejs/agenthistory/history.js, which mirrors
// demo/agent-boardroom/fixtures/support-desk.js's wire contract (the
// committed Task 6 reference implementation of the append path) — and the
// agent runtime's registry introspection surface (GET .../sessions/{id}/
// history, GET .../sessions/{id}/checkpoint) must reflect those writes.
//
// Two turns on one session id suffice: the fixture writes turn_start+yield
// per turn (4 events total across both turns) and a checkpoint on the
// SECOND turn (0-based turn=1; the fixture's CHECKPOINT_EVERY=2, half the
// demo's every-5th-turn cadence, since this is a wire-path test, not the
// demo's density story).
//
// Like TestFunctionState, this needs the real statesvc-backed function-state
// token, which only reaches function pods under static tenancy — skip
// rather than assert a known-unsupported combination on the
// dynamic/cluster-tenancy CI legs (same TenancyMode guard, same reasoning).
func TestAgentRuntimeHistory(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	requireAgentRuntimeReachable(t, ctx, f)
	stateSvcReachableOrSkip(t, ctx, f)
	if mode := f.TenancyMode(t, ctx); mode != "static" {
		t.Skipf("function state is static-tenancy only for now; tenancy mode is %q (per-namespace state key is a follow-up)", mode)
	}

	ns := f.NewTestNamespace(t)
	envName := "nodejs-agent-history-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	fnName := "agent-history-" + ns.ID
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name:  fnName,
		Env:   envName,
		Code:  framework.WriteTestData(t, "nodejs/agenthistory/history.js"),
		State: true,
	})
	ns.WaitForFunction(t, ctx, fnName)
	// FunctionOptions has no Agent field (see TestAgentRuntimeSessionDispatch's
	// comment on the same workaround); this leaves Spec.State intact
	// (update.go passes the existing State config through when no
	// --state* flags are given on the update).
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--agent")

	base := f.AgentRuntimeBaseURL()
	turnURL := base + "/agents/" + ns.Name + "/" + fnName

	// First turn: no session header. Mints a session id (0-based turn 0 on
	// the dispatcher's HeaderTurns), same shape as
	// TestAgentRuntimeSessionDispatch's first turn.
	var sessionID string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postTurn(attemptCtx, f, turnURL, "")
		if !assert.NoErrorf(c, err, "POST %s", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "first turn to %s", turnURL) {
			return
		}
		sid := resp.Header.Get(agentruntime.HeaderSession)
		if !assert.NotEmpty(c, sid, "first turn must mint a session id via %s", agentruntime.HeaderSession) {
			return
		}
		sessionID = sid
	}, 180*time.Second, 2*time.Second)
	require.NotEmpty(t, sessionID, "first turn never minted a session id")

	// Second turn: same session id explicitly (0-based turn 1). This is the
	// turn the fixture checkpoints on (CHECKPOINT_EVERY=2).
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postTurn(attemptCtx, f, turnURL, sessionID)
		if !assert.NoErrorf(c, err, "POST %s", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "second turn to %s", turnURL) {
			return
		}
		assert.Equal(c, sessionID, resp.Header.Get(agentruntime.HeaderSession),
			"second turn must echo the caller-supplied session id, not mint a new one")
	}, 180*time.Second, 2*time.Second)

	// The registry history must reflect both turns' events: at least 4 (2
	// per turn), the first event's payload carrying turn:0 and the last
	// carrying turn:1, head advanced past 0, and both event kinds present.
	historyURL := base + "/registry/agents/" + ns.Name + "/" + fnName + "/sessions/" + sessionID + "/history"
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, registryAttemptTimeout)
		defer cancel()
		body, status, err := getRegistry(attemptCtx, f, historyURL)
		if !assert.NoErrorf(c, err, "GET %s", historyURL) {
			return
		}
		if !assert.Equalf(c, http.StatusOK, status, "GET %s (body=%s)", historyURL, body) {
			return
		}
		var payload historyResponseDTO
		if !assert.NoErrorf(c, json.Unmarshal(body, &payload), "decoding %s body %q", historyURL, body) {
			return
		}
		if !assert.GreaterOrEqualf(c, len(payload.Events), 4,
			"session %s should have at least 4 history events (2 per turn x 2 turns), got %d", sessionID, len(payload.Events)) {
			return
		}
		assert.Greaterf(c, payload.Head, int64(0), "history head must have advanced past 0, got %d", payload.Head)

		// Both turns' events must be present in the history — but their
		// relative ORDER is not asserted: the dispatcher makes no promise
		// that turn 0's events are ordered strictly before turn 1's beyond
		// what the EventLog's own append-order provides, so pinning "first
		// event is turn:0, last is turn:1" would be asserting more than the
		// contract guarantees. Presence of both turns is the real claim.
		seenTurns := make(map[int]bool, len(payload.Events))
		for _, e := range payload.Events {
			turn, ok := decodeEventTurn(c, e)
			if !ok {
				return
			}
			seenTurns[turn] = true
		}
		assert.Truef(c, seenTurns[0], "history events must include a turn:0 event, got turns %+v", seenTurns)
		assert.Truef(c, seenTurns[1], "history events must include a turn:1 event, got turns %+v", seenTurns)

		kinds := make(map[string]bool, len(payload.Events))
		for _, e := range payload.Events {
			kinds[e.Type] = true
		}
		assert.Truef(c, kinds["turn_start"], "history events must include a turn_start event, got types %+v", kinds)
		assert.Truef(c, kinds["yield"], "history events must include a yield event, got types %+v", kinds)
	}, 60*time.Second, 2*time.Second)

	// The checkpoint written on the second turn (0-based turn=1) must be
	// visible through the registry checkpoint route.
	checkpointURL := base + "/registry/agents/" + ns.Name + "/" + fnName + "/sessions/" + sessionID + "/checkpoint"
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, registryAttemptTimeout)
		defer cancel()
		body, status, err := getRegistry(attemptCtx, f, checkpointURL)
		if !assert.NoErrorf(c, err, "GET %s", checkpointURL) {
			return
		}
		if !assert.Equalf(c, http.StatusOK, status, "GET %s (body=%s)", checkpointURL, body) {
			return
		}
		var payload checkpointDTO
		if !assert.NoErrorf(c, json.Unmarshal(body, &payload), "decoding %s body %q", checkpointURL, body) {
			return
		}
		assert.Greaterf(c, payload.CoveredThroughSeq, int64(0), "checkpoint coveredThroughSeq must be > 0, got %d", payload.CoveredThroughSeq)
		assert.Equalf(c, int64(1), payload.Turn, "checkpoint must have been written at 0-based turn 1 (fixture CHECKPOINT_EVERY=2), got %d", payload.Turn)
	}, 60*time.Second, 2*time.Second)
}

// sessionsListDTO decodes ListSessions's response
// (pkg/agentruntime/registry_api.go's unexported sessionsResponse) — Sessions
// is typed as the real agentruntime.SessionRecord (exported, same package
// this test already imports) rather than a hand-duplicated DTO, since its
// wire shape (including the Parent/Depth fields this test asserts on) is
// exactly what GetSession/ListSessions marshal.
type sessionsListDTO struct {
	Sessions []agentruntime.SessionRecord `json:"sessions"`
	Next     string                       `json:"next,omitempty"`
}

// postAgentTurn POSTs one turn to turnURL carrying sessionID on
// agentruntime.HeaderSession when non-empty, parentHeader on
// agentruntime.HeaderParent when non-empty, and body as the raw JSON
// request body. Generalizes postTurn (fixed "{}" body, no parent header) for
// TestAgentRuntimeSpawnParentage's needs: turns that ask the fixture to spawn
// ({"spawn":true}) and depth-chain turns dispatched directly by this test
// with only a parent header, no fixture-side spawning involved at all.
func postAgentTurn(ctx context.Context, f *framework.Framework, turnURL, sessionID, parentHeader, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(agentruntime.HeaderSession, sessionID)
	}
	if parentHeader != "" {
		req.Header.Set(agentruntime.HeaderParent, parentHeader)
	}
	return f.HTTPClient().Do(req)
}

// TestAgentRuntimeSpawnParentage exercises RFC-0031 slice 5's session
// parentage wire path end-to-end against a real cluster (Task 5 of the
// parent-graph plan): pkg/agentruntime/dispatcher.go's X-Fission-Agent-Parent
// creation-only header handling and session.go's ParentRef/SpawnSessionID
// derivation, GetSession's parent/depth wire shape, and the live 400 the
// spawn-depth cap returns.
//
// The fixture (test/integration/testdata/nodejs/agentspawn/spawn.js) is a
// minimal cousin of demo/agent-boardroom/fixtures/architect.js: on a turn
// whose body carries {"spawn":true} it spawns exactly ONE child session on
// ITSELF for stepKey "expert-1", using a byte-identical JS twin of
// SpawnSessionID.
//
// Four things are asserted, in order:
//
//  1. Cross-language id agreement (the load-bearing assertion): this test
//     computes expected := agentruntime.SpawnSessionID(parentRef, "expert-1")
//     in Go and GETs EXACTLY that id from the registry — 200, with parent
//     equal to the parent's triple and depth == 1. It also compares expected
//     against the fixture's own reported "spawned" id, so a derivation drift
//     on either side (hash, prefix, truncation length) fails with a message
//     naming which side disagreed, not just a registry 404.
//  2. Idempotent spawn: re-dispatching the parent's spawn turn a second time
//     must not mint a second child — ListSessions must still show exactly one
//     session whose parent.id is the parent session.
//  3. Root-degrade: a fresh session dispatched with a parent header naming a
//     NONEXISTENT session must still succeed (200, root-admitted per
//     resolveParentage's "parent claim dropped" fallback) and its registry
//     record must carry no parent and depth 0.
//  4. Depth cap: chaining sessions directly (this test dispatches turns to
//     each derived id itself, carrying only a parent header — no fixture-side
//     spawning needed, since resolveParentage runs entirely off the STORED
//     parent record before the function is ever invoked) from the depth-0
//     parent through depth 1, 2, 3 succeeds; the depth-4 attempt must return
//     HTTP 400 (AGENT_MAX_SPAWN_DEPTH is unset in CI, so the default cap of 3
//     makes depth 4 the first rejection).
func TestAgentRuntimeSpawnParentage(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	requireAgentRuntimeReachable(t, ctx, f)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-agent-spawn-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	fnName := "agent-spawn-" + ns.ID
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName,
		Env:  envName,
		Code: framework.WriteTestData(t, "nodejs/agentspawn/spawn.js"),
		// The fixture spawns children on ITSELF: it needs to know its own
		// namespace and function name to build both the outbound dispatch
		// URL and the ParentRef header it stamps on the child. This test
		// already knows both before creating the function, so it passes
		// them as plain env vars rather than making the fixture depend on
		// the (unrelated) RFC-0023 state-token file the way architect.js
		// does.
		EnvVars: []string{
			"SELF_NAMESPACE=" + ns.Name,
			"SELF_AGENT_NAME=" + fnName,
		},
	})
	ns.WaitForFunction(t, ctx, fnName)
	// Same CLI-update workaround as the other tests in this file
	// (FunctionOptions has no Agent field yet).
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--agent")

	base := f.AgentRuntimeBaseURL()
	turnURL := base + "/agents/" + ns.Name + "/" + fnName

	// Turn 1: no session header, no spawn. Mints the parent (root, depth 0)
	// session this whole test hangs off of.
	var parentSessionID string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postAgentTurn(attemptCtx, f, turnURL, "", "", `{}`)
		if !assert.NoErrorf(c, err, "POST %s", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "root turn to %s", turnURL) {
			return
		}
		sid := resp.Header.Get(agentruntime.HeaderSession)
		if !assert.NotEmpty(c, sid, "root turn must mint a session id via %s", agentruntime.HeaderSession) {
			return
		}
		parentSessionID = sid
	}, 180*time.Second, 2*time.Second)
	require.NotEmpty(t, parentSessionID, "root turn never minted a session id")

	parentRef := agentruntime.ParentRef{Namespace: ns.Name, Agent: fnName, ID: parentSessionID}
	expectedChildID := agentruntime.SpawnSessionID(parentRef, "expert-1")
	childURL := base + "/registry/agents/" + ns.Name + "/" + fnName + "/sessions/" + expectedChildID

	// Turn 2 on the parent session: {"spawn":true} asks the fixture to spawn
	// its one child. The fixture's inner dispatch to the child is a separate,
	// best-effort network hop from inside the pod (spawn.js's spawnChild doc
	// comment) that this outer turn's own 200 does not reflect the success
	// of — so a transient failure there would otherwise strand a
	// registry-only retry loop polling for a record that never arrives. This
	// loop instead re-issues the WHOLE spawn turn on every attempt, giving a
	// flaky inner dispatch further attempts to actually land.
	var reportedSpawnID string
	var childRec agentruntime.SessionRecord
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postAgentTurn(attemptCtx, f, turnURL, parentSessionID, "", `{"spawn":true}`)
		if !assert.NoErrorf(c, err, "POST %s (spawn turn)", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "spawn turn to %s", turnURL) {
			return
		}
		turnBody, err := io.ReadAll(resp.Body)
		if !assert.NoErrorf(c, err, "reading spawn turn response body") {
			return
		}
		var payload struct {
			Spawned string `json:"spawned"`
		}
		if !assert.NoErrorf(c, json.Unmarshal(turnBody, &payload), "decoding spawn turn response body %q", turnBody) {
			return
		}
		reportedSpawnID = payload.Spawned

		registryCtx, registryCancel := context.WithTimeout(ctx, registryAttemptTimeout)
		defer registryCancel()
		regBody, status, err := getRegistry(registryCtx, f, childURL)
		if !assert.NoErrorf(c, err, "GET %s", childURL) {
			return
		}
		if !assert.Equalf(c, http.StatusOK, status, "GET %s (body=%s)", childURL, regBody) {
			return
		}
		if !assert.NoErrorf(c, json.Unmarshal(regBody, &childRec), "decoding %s body %q", childURL, regBody) {
			return
		}
	}, 180*time.Second, 3*time.Second)
	require.NotEmptyf(t, reportedSpawnID, "spawn turn on session %s never reported a spawned id", parentSessionID)

	// 1. CROSS-LANGUAGE AGREEMENT: Go's agentruntime.SpawnSessionID and the
	// fixture's JS twin must derive the SAME id from the SAME parent+stepKey.
	assert.Equalf(t, expectedChildID, reportedSpawnID,
		"cross-language derivation mismatch: Go agentruntime.SpawnSessionID(%+v, %q) = %s, fixture spawnSessionID reported %s",
		parentRef, "expert-1", expectedChildID, reportedSpawnID)

	// And the registry shows a session record living at EXACTLY that id,
	// admitted with the parent's triple and depth 1 — this is what actually
	// proves the two independent derivations converged on a real admission
	// row, not just matching strings.
	if assert.NotNilf(t, childRec.Parent, "child session %s must carry a parent, got nil", expectedChildID) {
		assert.Equalf(t, parentRef, *childRec.Parent, "child session %s parent mismatch", expectedChildID)
	}
	assert.Equalf(t, 1, childRec.Depth, "child session %s must be depth 1, got %d", expectedChildID, childRec.Depth)

	// 2. IDEMPOTENT SPAWN: re-dispatch the parent's spawn turn. The fixture
	// derives the SAME childID (same parent session, same stepKey) and
	// spawns again — the dispatcher's Create(IfVersion=0) semantics on the
	// child mean this resumes the existing child record rather than minting
	// a second one. List the function's sessions and confirm exactly ONE
	// carries this parent.
	//
	// This MUST run before part 4's depth chain: that chain's first hop
	// (chainParent = parentRef, depth 0) is ALSO a direct child of
	// parentSessionID, so counting "children of parentSessionID" after the
	// chain exists would no longer isolate this assertion to the fixture's
	// own spawn.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postAgentTurn(attemptCtx, f, turnURL, parentSessionID, "", `{"spawn":true}`)
		if !assert.NoErrorf(c, err, "POST %s (second spawn turn)", turnURL) {
			return
		}
		defer resp.Body.Close()
		assert.Equalf(c, http.StatusOK, resp.StatusCode, "second spawn turn to %s", turnURL)
	}, 180*time.Second, 2*time.Second)

	sessionsURL := base + "/registry/agents/" + ns.Name + "/" + fnName + "/sessions"
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, registryAttemptTimeout)
		defer cancel()
		body, status, err := getRegistry(attemptCtx, f, sessionsURL)
		if !assert.NoErrorf(c, err, "GET %s", sessionsURL) {
			return
		}
		if !assert.Equalf(c, http.StatusOK, status, "GET %s (body=%s)", sessionsURL, body) {
			return
		}
		var payload sessionsListDTO
		if !assert.NoErrorf(c, json.Unmarshal(body, &payload), "decoding %s body %q", sessionsURL, body) {
			return
		}
		children := 0
		for _, s := range payload.Sessions {
			if s.Parent != nil && s.Parent.ID == parentSessionID {
				children++
			}
		}
		assert.Equalf(c, 1, children,
			"session %s must have exactly one child after re-dispatching the spawn turn (idempotent spawn), got %d (sessions=%+v)",
			parentSessionID, children, payload.Sessions)
	}, 60*time.Second, 2*time.Second)

	// 3. ROOT-DEGRADE: a fresh session dispatched with a parent header naming
	// a session that does not exist must still be admitted (200), as a root
	// (no parent, depth 0) — resolveParentage's "parent claim dropped,
	// admitting as root" fallback, not an error surfaced to the caller.
	rootDegradeSessionID := "root-degrade-" + ns.ID
	fakeParentHeader := ns.Name + "/" + fnName + "/does-not-exist-xyz"
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postAgentTurn(attemptCtx, f, turnURL, rootDegradeSessionID, fakeParentHeader, `{}`)
		if !assert.NoErrorf(c, err, "POST %s (root-degrade turn)", turnURL) {
			return
		}
		defer resp.Body.Close()
		assert.Equalf(c, http.StatusOK, resp.StatusCode,
			"a turn naming a nonexistent parent must still be admitted as root (200), not rejected")
	}, 180*time.Second, 2*time.Second)

	rootDegradeURL := base + "/registry/agents/" + ns.Name + "/" + fnName + "/sessions/" + rootDegradeSessionID
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, registryAttemptTimeout)
		defer cancel()
		body, status, err := getRegistry(attemptCtx, f, rootDegradeURL)
		if !assert.NoErrorf(c, err, "GET %s", rootDegradeURL) {
			return
		}
		if !assert.Equalf(c, http.StatusOK, status, "GET %s (body=%s)", rootDegradeURL, body) {
			return
		}
		var rec agentruntime.SessionRecord
		if !assert.NoErrorf(c, json.Unmarshal(body, &rec), "decoding %s body %q", rootDegradeURL, body) {
			return
		}
		assert.Nilf(c, rec.Parent, "session %s named a nonexistent parent, so it must carry NO parent, got %+v", rootDegradeSessionID, rec.Parent)
		assert.Equalf(c, 0, rec.Depth, "session %s named a nonexistent parent, so it must be depth 0, got %d", rootDegradeSessionID, rec.Depth)
	}, 60*time.Second, 2*time.Second)

	// 4. DEPTH CAP: chain sessions directly from the depth-0 parent —
	// resolveParentage computes depth from the STORED parent record alone,
	// before the function is ever invoked, so this test can dispatch each
	// hop itself with just a parent header; no fixture-side spawning is
	// needed for this part at all. AGENT_MAX_SPAWN_DEPTH is unset in CI, so
	// the default cap (3) makes a would-be depth-4 session the first
	// rejection.
	chainStepKey := "chain"
	chainParent := parentRef // depth 0
	for depth := 1; depth <= 3; depth++ {
		childID := agentruntime.SpawnSessionID(chainParent, chainStepKey)
		parentHeader := chainParent.String()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
			defer cancel()
			resp, err := postAgentTurn(attemptCtx, f, turnURL, childID, parentHeader, `{}`)
			if !assert.NoErrorf(c, err, "POST %s (depth-%d chain turn)", turnURL, depth) {
				return
			}
			defer resp.Body.Close()
			assert.Equalf(c, http.StatusOK, resp.StatusCode, "depth-%d chain turn (session %s, parent %s) must be admitted", depth, childID, parentHeader)
		}, 180*time.Second, 2*time.Second)
		chainParent = agentruntime.ParentRef{Namespace: ns.Name, Agent: fnName, ID: childID}
	}

	depth4ParentHeader := chainParent.String() // chainParent is now the depth-3 session
	depth4ChildID := agentruntime.SpawnSessionID(chainParent, chainStepKey)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postAgentTurn(attemptCtx, f, turnURL, depth4ChildID, depth4ParentHeader, `{}`)
		if !assert.NoErrorf(c, err, "POST %s (depth-4 chain turn)", turnURL) {
			return
		}
		defer resp.Body.Close()
		assert.Equalf(c, http.StatusBadRequest, resp.StatusCode,
			"depth-4 spawn (session %s, parent %s) must be rejected — AGENT_MAX_SPAWN_DEPTH defaults to 3 in CI", depth4ChildID, depth4ParentHeader)
	}, 60*time.Second, 2*time.Second)
}

// nextSSEFrame reads one SSE message from r: comment lines (starting with
// ':', e.g. the ": keepalive" lines events.go's ServeHTTP writes) are
// skipped entirely, then an "event:" line and a "data:" line are read up to
// the frame's terminating blank line, mirroring the exact
// "event: %s\ndata: %s\n\n" shape writeSSEEvent (events.go) writes. It
// returns the underlying read error (io.EOF at stream end, or a
// context-deadline error once the request's context is done) when a full
// frame cannot be read.
func nextSSEFrame(r *bufio.Reader) (event, data string, err error) {
	for {
		line, rerr := r.ReadString('\n')
		if rerr != nil {
			return "", "", rerr
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue // a stray blank line between frames
		case strings.HasPrefix(trimmed, ":"):
			// Comment/keepalive frame: it carries no event:/data: lines of
			// its own, just its own terminating blank line — consume that
			// and keep looking for the next real frame.
			if _, rerr := r.ReadString('\n'); rerr != nil {
				return "", "", rerr
			}
			continue
		case strings.HasPrefix(trimmed, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		default:
			continue
		}
		dataLine, rerr := r.ReadString('\n')
		if rerr != nil {
			return "", "", rerr
		}
		data = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(dataLine), "data:"))
		// Consume the frame's terminating blank line; tolerate it being
		// missing (e.g. the stream ends right after this frame) since the
		// frame itself was already read successfully.
		_, _ = r.ReadString('\n')
		return event, data, nil
	}
}

// sseFrameMatchesSession reports whether a "session" SSE frame's data
// carries a record whose id equals wantSessionID (agentruntime.
// sessionEvent's wire shape, events.go).
func sseFrameMatchesSession(event, data, wantSessionID string) bool {
	if event != "session" || data == "" {
		return false
	}
	var payload struct {
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	return json.Unmarshal([]byte(data), &payload) == nil && payload.Record.ID == wantSessionID
}

// getRegistry issues a plain authenticated GET to url (via f.HTTPClient(),
// same transport postTurn uses) and returns the response body and status
// code. Mirrors postTurn's shape for the read-only registry routes.
func getRegistry(ctx context.Context, f *framework.Framework, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := f.HTTPClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// postTurn POSTs one turn to turnURL, carrying sessionID on
// agentruntime.HeaderSession when non-empty. The caller is responsible for
// closing the response body on a nil error.
func postTurn(ctx context.Context, f *framework.Framework, turnURL, sessionID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnURL, strings.NewReader(`{}`))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(agentruntime.HeaderSession, sessionID)
	}
	return f.HTTPClient().Do(req)
}

// requireAgentRuntimeReachable skips the test when the agent runtime endpoint
// isn't serving (agent runtime disabled in this install). Mirrors
// requireMCPReachable in mcp_test.go: a short first probe distinguishes "not
// installed" (typed target-not-found — skip fast) from "warming" (earns the
// generous deadline a cold in-process port-forward needs).
func requireAgentRuntimeReachable(t *testing.T, ctx context.Context, f *framework.Framework) {
	t.Helper()
	base := f.AgentRuntimeBaseURL()
	probe := func(timeout time.Duration) (int, error) {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/healthz", nil)
		require.NoError(t, err)
		resp, err := f.HTTPClient().Do(req)
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	status, err := probe(5 * time.Second)
	if framework.IsTargetMissing(err) {
		t.Skipf("agent runtime not installed (svc/agentruntime absent); skipping: %v", err)
	}
	if err != nil {
		status, err = probe(30 * time.Second)
	}
	if err != nil {
		t.Skipf("agent runtime endpoint %s not reachable (%v); skipping", base, err)
	}
	if status != http.StatusOK {
		t.Skipf("agent runtime endpoint %s returned %d; skipping", base, status)
	}
}
