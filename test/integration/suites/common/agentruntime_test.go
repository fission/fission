// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
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
	attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
	defer cancel()
	resp, err := postTurn(attemptCtx, f, turnURL, sessionID)
	require.NoErrorf(t, err, "POST %s", turnURL)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, sessionID, resp.Header.Get(agentruntime.HeaderSession),
		"second turn must echo the caller-supplied session id, not mint a new one")
	assert.Empty(t, resp.Header.Get(agentruntime.HeaderYield))
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
