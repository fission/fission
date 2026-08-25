// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// recordedSessionReq captures one request the mock agent runtime received.
type recordedSessionReq struct {
	Method string
	Path   string
	Query  string
	Auth   string
}

// mockAgentRuntime starts an httptest.Server standing in for the
// agentruntime's RegistryAPI, points FISSION_AGENTRUNTIME_URL at it (the
// same env-var override agentRuntimeBaseURL checks before ever trying a
// port-forward), and records every request it receives.
func mockAgentRuntime(t *testing.T, status int, body string) *[]recordedSessionReq {
	t.Helper()
	var got []recordedSessionReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, recordedSessionReq{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Auth:   r.Header.Get("Authorization"),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FISSION_AGENTRUNTIME_URL", srv.URL)
	return &got
}

const twoSessionsFixture = `{"sessions":[
	{"id":"sess-1","agent":"agentfn","namespace":"testns","status":"active",
	 "createdAt":"2026-08-20T09:00:00Z","lastActiveAt":"2026-08-20T10:05:00Z",
	 "currentPod":"agentfn-abc123",
	 "stats":{"turns":3,"errors":0,"toolCalls":5,"activeMillis":1200,"tokensIn":100,"tokensOut":200,"costMicroUSD":50,"continuations":1}},
	{"id":"sess-2","agent":"agentfn","namespace":"testns","status":"idle",
	 "createdAt":"2026-08-20T08:00:00Z",
	 "stats":{"turns":0,"continuations":0}}
]}`

// TestSessionsListRendersColumns exercises `fission agent sessions list`
// against a fixture with two sessions: one with every field populated, one
// relying on documented zero-value defaults (no lastActiveAt, no
// currentPod), which must render as "-" per the ID/STATUS/TURNS/
// CONTINUATIONS/LAST-ACTIVE/POD column contract.
func TestSessionsListRendersColumns(t *testing.T) {
	got := mockAgentRuntime(t, http.StatusOK, twoSessionsFixture)

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "agentfn"),
		dummy.String(flagkey.Namespace, "testns"),
	)
	out := captureStdout(t, func() error { return SessionsList(in) })

	require.Len(t, *got, 1)
	req := (*got)[0]
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/registry/agents/testns/agentfn/sessions", req.Path)
	assert.Equal(t, "limit=500", req.Query, "list asks for the server's max page (500), not its lower default (100)")
	assert.Empty(t, req.Auth, "no --agent-token/FISSION_AGENT_TOKEN set: request must go out unauthenticated")

	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "STATUS")
	assert.Contains(t, out, "TURNS")
	assert.Contains(t, out, "CONTINUATIONS")
	assert.Contains(t, out, "LAST-ACTIVE")
	assert.Contains(t, out, "POD")

	assert.Contains(t, out, "sess-1")
	assert.Contains(t, out, "active")
	assert.Contains(t, out, "agentfn-abc123")

	assert.Contains(t, out, "sess-2")
	assert.Contains(t, out, "idle")

	// sess-2's unset LastActiveAt/CurrentPod must render as "-", not an
	// empty cell (which would misalign the table) or a zero timestamp.
	lines := splitNonEmptyLines(out)
	require.Len(t, lines, 3, "header + 2 rows")
	sess2Row := lines[2]
	assert.Contains(t, sess2Row, "-", "sess-2's blank fields render as dashes")
	assert.NotContains(t, sess2Row, "0001-01-01", "zero time.Time must never leak into the table")
}

// TestSessionsListJSONStaysParseableWithMorePages is the regression case for
// the format/warning ordering bug: when the server reports a Next cursor,
// `-o json` must still be exactly the sessions payload on stdout — no
// console.Warn line ahead of it — so a consumer piping into `jq` never sees
// a parse error just because the agent has more sessions than one page.
func TestSessionsListJSONStaysParseableWithMorePages(t *testing.T) {
	const bodyWithNext = `{"sessions":[{"id":"sess-1","agent":"agentfn","namespace":"testns","status":"active",
		"createdAt":"2026-08-20T09:00:00Z","lastActiveAt":"2026-08-20T10:05:00Z","stats":{"turns":1}}],
		"next":"sess-2"}`
	mockAgentRuntime(t, http.StatusOK, bodyWithNext)

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "agentfn"),
		dummy.String(flagkey.Namespace, "testns"),
		dummy.String(flagkey.Output, "json"),
	)
	out := captureStdout(t, func() error { return SessionsList(in) })

	require.NotContains(t, out, "Warning", "a structured format must never carry the more-pages notice on stdout")

	var decoded []sessionRecord
	require.NoError(t, json.Unmarshal([]byte(out), &decoded), "stdout must be valid JSON end to end: %s", out)
	require.Len(t, decoded, 1)
	assert.Equal(t, "sess-1", decoded[0].ID)
}

// familyTreeFixture is a single page (no "next") carrying a 3-node spawn
// family (root-1 -> child-1 -> grand-1) plus orphan-1, whose parent ref
// ("missing-parent") is not among the fetched records — the case the
// "(unknown: ...)" node exists to make visible instead of silently dropping.
const familyTreeFixture = `{"sessions":[
	{"id":"root-1","agent":"agentfn","namespace":"testns","status":"active",
	 "createdAt":"2026-08-20T09:00:00Z","stats":{"turns":1}},
	{"id":"child-1","agent":"agentfn","namespace":"testns","status":"active",
	 "createdAt":"2026-08-20T09:05:00Z","stats":{"turns":2},
	 "parent":{"namespace":"testns","agent":"agentfn","id":"root-1"},"depth":1},
	{"id":"grand-1","agent":"agentfn","namespace":"testns","status":"active",
	 "createdAt":"2026-08-20T09:10:00Z","stats":{"turns":3},
	 "parent":{"namespace":"testns","agent":"agentfn","id":"child-1"},"depth":2},
	{"id":"orphan-1","agent":"agentfn","namespace":"testns","status":"idle",
	 "createdAt":"2026-08-20T09:15:00Z","stats":{"turns":0},
	 "parent":{"namespace":"testns","agent":"agentfn","id":"missing-parent"},"depth":1}
]}`

// TestSessionsListTreeRendersFamily exercises `fission agent sessions list
// --tree` against a fabricated 3-generation family plus one orphan, checking
// the indentation nests children under their parent, in parent-before-child
// order, and that the orphan renders under an explicit "(unknown: ...)" node
// (naming the missing parent's full <ns>/<agent>/<id> triple) rather than
// being silently dropped.
func TestSessionsListTreeRendersFamily(t *testing.T) {
	mockAgentRuntime(t, http.StatusOK, familyTreeFixture)

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "agentfn"),
		dummy.String(flagkey.Namespace, "testns"),
	)
	in.SetBool(flagkey.AgentSessionsTree, true)
	out := captureStdout(t, func() error { return SessionsList(in) })

	lines := splitNonEmptyLines(out)
	require.Len(t, lines, 6, "header + root-1 + child-1 + grand-1 + (unknown: ...) + orphan-1:\n%s", out)

	rootLine := findLine(t, lines, "root-1")
	childLine := findLine(t, lines, "child-1")
	grandLine := findLine(t, lines, "grand-1")
	unknownLine := findLine(t, lines, "(unknown:")
	orphanLine := findLine(t, lines, "orphan-1")

	assert.True(t, strings.HasPrefix(rootLine, "root-1"), "root has no indent: %q", rootLine)
	assert.True(t, strings.HasPrefix(childLine, "  child-1"), "child indented 2 spaces under its parent: %q", childLine)
	assert.True(t, strings.HasPrefix(grandLine, "    grand-1"), "grandchild indented 4 spaces: %q", grandLine)
	assert.Contains(t, unknownLine, "(unknown: testns/agentfn/missing-parent)")
	assert.True(t, strings.HasPrefix(orphanLine, "  orphan-1"), "orphan rendered at its own depth under the unknown node: %q", orphanLine)

	// Parent-before-child ordering, and the unknown group after the real
	// family tree.
	assert.Less(t, indexOf(lines, rootLine), indexOf(lines, childLine))
	assert.Less(t, indexOf(lines, childLine), indexOf(lines, grandLine))
	assert.Less(t, indexOf(lines, grandLine), indexOf(lines, unknownLine))
	assert.Less(t, indexOf(lines, unknownLine), indexOf(lines, orphanLine))
}

// TestSessionsListTreePaginatesToExhaustion checks that --tree loops the
// server's page cursor to exhaustion before building the tree: a fake server
// answers the first request with one session and a "next" cursor, and the
// second (carrying that cursor) with a final session and no cursor. Both
// pages' sessions must appear in the rendered output, and the plain
// (non-tree) single-page behavior this regresses against is covered by
// TestSessionsListRendersColumns.
func TestSessionsListTreePaginatesToExhaustion(t *testing.T) {
	var reqs []recordedSessionReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, recordedSessionReq{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Auth:   r.Header.Get("Authorization"),
		})
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "" {
			_, _ = w.Write([]byte(`{"sessions":[{"id":"page1-a","agent":"agentfn","namespace":"testns","status":"active",
				"createdAt":"2026-08-20T09:00:00Z","stats":{"turns":1}}],"next":"cursor-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"sessions":[{"id":"page2-a","agent":"agentfn","namespace":"testns","status":"active",
			"createdAt":"2026-08-20T09:05:00Z","stats":{"turns":2}}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FISSION_AGENTRUNTIME_URL", srv.URL)

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "agentfn"),
		dummy.String(flagkey.Namespace, "testns"),
	)
	in.SetBool(flagkey.AgentSessionsTree, true)
	out := captureStdout(t, func() error { return SessionsList(in) })

	require.Len(t, reqs, 2, "must loop the page cursor until the server reports no next page")
	assert.Equal(t, "limit=500", reqs[0].Query)
	assert.Equal(t, "limit=500&page=cursor-1", reqs[1].Query)

	assert.Contains(t, out, "page1-a")
	assert.Contains(t, out, "page2-a")
}

// findLine returns the first line in lines containing substr, failing the
// test if none does.
func findLine(t *testing.T, lines []string, substr string) string {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(line, substr) {
			return line
		}
	}
	t.Fatalf("no output line contains %q:\n%s", substr, strings.Join(lines, "\n"))
	return ""
}

// indexOf returns s's index in lines (exact match).
func indexOf(lines []string, s string) int {
	for i, line := range lines {
		if line == s {
			return i
		}
	}
	return -1
}

// TestSessionsGetRendersDetail exercises `fission agent sessions get`
// against a single-record fixture and checks the request hits the
// {namespace}/{name}/sessions/{id} route with every stat rendered.
func TestSessionsGetRendersDetail(t *testing.T) {
	const body = `{"id":"sess-1","agent":"agentfn","namespace":"testns","status":"active",
		"createdAt":"2026-08-20T09:00:00Z","lastActiveAt":"2026-08-20T10:05:00Z",
		"currentPod":"agentfn-abc123",
		"stats":{"turns":7,"errors":1,"toolCalls":2,"activeMillis":500,"tokensIn":10,"tokensOut":20,"costMicroUSD":5,"continuations":2}}`
	got := mockAgentRuntime(t, http.StatusOK, body)

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "agentfn"),
		dummy.String(flagkey.AgentSession, "sess-1"),
		dummy.String(flagkey.Namespace, "testns"),
	)
	out := captureStdout(t, func() error { return SessionsGet(in) })

	require.Len(t, *got, 1)
	assert.Equal(t, "/registry/agents/testns/agentfn/sessions/sess-1", (*got)[0].Path)

	assert.Contains(t, out, "sess-1")
	assert.Contains(t, out, "active")
	assert.Contains(t, out, "agentfn-abc123")
	// tabwriter expands '\t' into aligned spaces, so match label+value with a
	// permissive gap rather than the literal '\t' the code writes.
	assertLabelValue(t, out, "TURNS:", "7")
	assertLabelValue(t, out, "ERRORS:", "1")
	assertLabelValue(t, out, "TOOL-CALLS:", "2")
	assertLabelValue(t, out, "CONTINUATIONS:", "2")
	assertLabelValue(t, out, "TOKENS-IN:", "10")
	assertLabelValue(t, out, "TOKENS-OUT:", "20")
	assertLabelValue(t, out, "COST-MICRO-USD:", "5")
}

// TestSessions404FriendlyMessage checks the registry's "not an agent
// function"/"session not found" 404 maps to a message naming the likely
// cause (agent/namespace mismatch) rather than a bare status code.
func TestSessions404FriendlyMessage(t *testing.T) {
	mockAgentRuntime(t, http.StatusNotFound, `{"error":"not an agent function"}`)

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "missingfn"),
		dummy.String(flagkey.Namespace, "testns"),
	)
	err := SessionsList(in)
	require.Error(t, err)
	assert.ErrorContains(t, err, "agent not found or has no session")
	assert.ErrorContains(t, err, "testns/missingfn")
	assert.ErrorContains(t, err, "not an agent function")
}

// TestSessions403FriendlyMessage checks the registry's authz-scope 403 maps
// to a message naming the token/namespace mismatch.
func TestSessions403FriendlyMessage(t *testing.T) {
	mockAgentRuntime(t, http.StatusForbidden, `{"error":"forbidden: token not authorized for any namespace"}`)

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "agentfn"),
		dummy.String(flagkey.Namespace, "testns"),
	)
	err := SessionsList(in)
	require.Error(t, err)
	assert.ErrorContains(t, err, "token not authorized for namespace testns")
}

// TestSessionsBearerHeader checks --agent-token is sent as an Authorization:
// Bearer header, and that it is omitted entirely (not an empty header) when
// unset — the unauthenticated path agentRuntime.allowInsecure relies on.
func TestSessionsBearerHeader(t *testing.T) {
	got := mockAgentRuntime(t, http.StatusOK, `{"sessions":[]}`)

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "agentfn"),
		dummy.String(flagkey.Namespace, "testns"),
		dummy.String(flagkey.AgentToken, "tok-123"),
	)
	captureStdout(t, func() error { return SessionsList(in) })
	require.Len(t, *got, 1)
	assert.Equal(t, "Bearer tok-123", (*got)[0].Auth)
}

// TestSessionsBearerHeaderFromEnv checks the FISSION_AGENT_TOKEN env
// fallback used when --agent-token is not passed.
func TestSessionsBearerHeaderFromEnv(t *testing.T) {
	got := mockAgentRuntime(t, http.StatusOK, `{"sessions":[]}`)
	t.Setenv("FISSION_AGENT_TOKEN", "env-tok")

	in := dummy.TestFlagSetWith(
		dummy.String(flagkey.FnName, "agentfn"),
		dummy.String(flagkey.Namespace, "testns"),
	)
	captureStdout(t, func() error { return SessionsList(in) })
	require.Len(t, *got, 1)
	assert.Equal(t, "Bearer env-tok", (*got)[0].Auth)
}

// assertLabelValue finds the "sessions get" detail line starting with label
// (tabwriter pads it with spaces to align the value column, so a literal
// "label:\tvalue" match would miss) and asserts its trailing field equals
// value.
func assertLabelValue(t *testing.T, out, label, value string) {
	t.Helper()
	for _, line := range splitNonEmptyLines(out) {
		if !strings.HasPrefix(line, label) {
			continue
		}
		fields := strings.Fields(line)
		require.NotEmpty(t, fields)
		assert.Equal(t, value, fields[len(fields)-1], "line %q", line)
		return
	}
	t.Fatalf("no output line starts with %q:\n%s", label, out)
}

// splitNonEmptyLines splits tabwriter output into its non-empty lines.
func splitNonEmptyLines(s string) []string {
	var out []string
	line := ""
	for _, r := range s {
		if r == '\n' {
			if line != "" {
				out = append(out, line)
			}
			line = ""
			continue
		}
		line += string(r)
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
