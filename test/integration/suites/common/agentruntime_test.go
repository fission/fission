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
	// `event: ...` frame — read it under a short deadline and close
	// promptly rather than draining the stream.
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

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoErrorf(t, err, "reading first SSE line from %s", eventsURL)
	assert.Truef(t, strings.HasPrefix(strings.TrimSpace(line), "event:"),
		"first SSE line from %s must be an event: frame (connect-time snapshot), got %q", eventsURL, line)
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
