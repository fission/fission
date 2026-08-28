// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/agentruntime"
	"github.com/fission/fission/test/integration/framework"
)

// execTurnReply is the exec verb's response contract:
// {"stdout": "...", "stderr": "...", "exitCode": 0, "durationMs": 12,
// "truncated": false}, extended by doc 14's PR-2 section with
// workspaceWarnings (the hydrate/sync helper's failure-reporting field).
// This has no shared Go struct on either side ("zero new platform Go" is the
// point of a template-only exec verb) -- it exists only to decode the wire
// response in this test.
type execTurnReply struct {
	Stdout            string   `json:"stdout"`
	Stderr            string   `json:"stderr"`
	ExitCode          int      `json:"exitCode"`
	DurationMs        int64    `json:"durationMs"`
	Truncated         bool     `json:"truncated"`
	WorkspaceWarnings []string `json:"workspaceWarnings"`
}

// sha256Hex is the "hashes equal" check doc 14's PR-2 acceptance criterion
// asks for verbatim ("the doc-13 mcp-server-sandbox persistence demo pattern
// (write -> suspend-equivalent -> read back, hashes equal) passes on
// Fission").
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// postExecTurn POSTs one turn carrying an arbitrary JSON payload (the exec
// verb's request body), unlike postTurn (agentruntime_test.go) which always
// sends "{}". Carries sessionID on agentruntime.HeaderSession when
// non-empty. The caller is responsible for closing the response body on a
// nil error.
func postExecTurn(ctx context.Context, f *framework.Framework, turnURL, sessionID string, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(agentruntime.HeaderSession, sessionID)
	}
	return f.HTTPClient().Do(req)
}

// dispatchExecTurn POSTs payload to turnURL (carrying sessionID when
// non-empty) and decodes a 200 response as execTurnReply, retrying inside
// EventuallyWithT so a cold pod / a still-converging agentruntime
// reconciler (same rationale as postTurn's callers throughout
// agentruntime_test.go) doesn't flake the assertion that follows.
func dispatchExecTurn(t *testing.T, ctx context.Context, f *framework.Framework, turnURL, sessionID string, payload []byte) (execTurnReply, string) {
	t.Helper()

	var reply execTurnReply
	var gotSessionID string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postExecTurn(attemptCtx, f, turnURL, sessionID, payload)
		if !assert.NoErrorf(c, err, "POST %s", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "exec turn to %s", turnURL) {
			return
		}
		sid := resp.Header.Get(agentruntime.HeaderSession)
		if !assert.NotEmpty(c, sid, "exec turn must carry a session id via %s", agentruntime.HeaderSession) {
			return
		}
		body, err := io.ReadAll(resp.Body)
		if !assert.NoErrorf(c, err, "reading exec turn response body") {
			return
		}
		if !assert.NoErrorf(c, json.Unmarshal(body, &reply), "decoding exec turn reply %q", body) {
			return
		}
		gotSessionID = sid
	}, 180*time.Second, 2*time.Second)
	return reply, gotSessionID
}

// TestAgentExec exercises the PR-1 (abstraction A, Q35 resolution) exec
// verb end to end: `fission agent create --template interpreter` scaffolds
// a handler that runs {"code": ...} / {"command": ...} turn bodies and
// answers {stdout, stderr, exitCode, durationMs, truncated} -- through the
// normal agent-runtime dispatch path, with zero platform-side awareness of
// this contract. Subtests cover four contract cases: a basic exec, the timeout ceiling, output truncation, and the
// env-scrub guarantee.
func TestAgentExec(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	requireAgentRuntimeReachable(t, ctx, f)

	ns := f.NewTestNamespace(t)
	envName := "agent-exec-node-" + ns.ID
	fnName := "agent-exec-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	workdir := t.TempDir()
	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		ns.CLI(t, ctx, "agent", "create", "--name", fnName, "--env", envName, "--template", "interpreter", "--spec")
		ns.CLI(t, ctx, "spec", "apply")
	})

	turnURL := f.AgentRuntimeBaseURL() + "/agents/" + ns.Name + "/" + fnName

	// First turn establishes the session every other subtest reuses --
	// mirrors TestAgentCreateScaffold/TestAgentRuntimeSessionDispatch's
	// no-session-header first call.
	basicReply, sessionID := dispatchExecTurn(t, ctx, f, turnURL, "", []byte(`{"code": "console.log(1 + 1)"}`))
	require.NotEmpty(t, sessionID, "scaffolded interpreter never minted a session id")

	t.Run("code executes and returns stdout/exitCode", func(t *testing.T) {
		assert.Equal(t, "2\n", basicReply.Stdout)
		assert.Empty(t, basicReply.Stderr)
		assert.Equal(t, 0, basicReply.ExitCode)
		assert.False(t, basicReply.Truncated)
		assert.GreaterOrEqual(t, basicReply.DurationMs, int64(0))
	})

	t.Run("command form executes through a shell", func(t *testing.T) {
		reply, sid := dispatchExecTurn(t, ctx, f, turnURL, sessionID, []byte(`{"command": "echo hello-from-exec"}`))
		assert.Equal(t, sessionID, sid)
		assert.Equal(t, "hello-from-exec\n", reply.Stdout)
		assert.Equal(t, 0, reply.ExitCode)
	})

	// Timeout case: a 5s sleep with a 1s ceiling must be killed (the WHOLE
	// process group, not merely the shell) and reported as exitCode -1 with
	// a note in stderr -- the contract's "kill the whole process group on expiry,
	// report exitCode: -1 + stderr note".
	t.Run("timeout kills the process and reports exitCode -1", func(t *testing.T) {
		reply, _ := dispatchExecTurn(t, ctx, f, turnURL, sessionID, []byte(`{"command": "sleep 5", "timeoutSeconds": 1}`))

		assert.Equal(t, -1, reply.ExitCode)
		assert.Contains(t, reply.Stderr, "timeout")
		// The process must actually have been killed near the 1s ceiling,
		// not run to completion (the sleep's own 5s), before the reply came
		// back. reply.DurationMs is measured template-side around the
		// subprocess call itself (see interpreter.{js,py}.tmpl) -- unlike a
		// wall-clock measurement wrapped around dispatchExecTurn's own
		// retrying poller (turnAttemptTimeout up to 60s per attempt, on a
		// 2s EventuallyWithT interval), it is immune to an unrelated
		// transient dispatch retry inflating the elapsed time and flaking
		// this assertion.
		assert.GreaterOrEqualf(t, reply.DurationMs, int64(900), "exec returned before the 1s timeout ceiling (durationMs=%d)", reply.DurationMs)
		assert.Lessf(t, reply.DurationMs, int64(4000), "exec did not appear to be killed at the timeout ceiling, not run to the sleep's own 5s (durationMs=%d)", reply.DurationMs)
	})

	// Truncation case: ask Node to write more than the 256KiB (262144-byte)
	// per-stream cap and confirm truncated:true plus a stdout capped at
	// EXACTLY that many bytes -- the contract's "stdout/stderr each capped at 256
	// KiB ... overflow sets truncated: true, never errors".
	t.Run("output over 256KiB is truncated, not errored", func(t *testing.T) {
		const overCap = 300000 // > 262144
		payload, err := json.Marshal(map[string]any{
			"code": "process.stdout.write('a'.repeat(" + strconv.Itoa(overCap) + "))",
		})
		require.NoError(t, err)

		reply, _ := dispatchExecTurn(t, ctx, f, turnURL, sessionID, payload)
		assert.True(t, reply.Truncated, "writing %d bytes of stdout must set truncated:true", overCap)
		assert.Equal(t, 0, reply.ExitCode)
		assert.Len(t, reply.Stdout, 256*1024, "truncated stdout must be capped at exactly 256KiB")
	})

	// Env-scrub case: the exec'd process must see NONE of this pod's
	// FISSION_* vars (state/agent token paths, runtime URLs) -- the contract's
	// "Subprocess env is allow-listed ... never pass-through". Dumping
	// process.env as JSON (rather than shelling out to `env`, which isn't
	// guaranteed present in every runtime image) is the code-form exec
	// path, exercised for its own sake as well.
	t.Run("subprocess env is scrubbed of FISSION_ vars", func(t *testing.T) {
		reply, _ := dispatchExecTurn(t, ctx, f, turnURL, sessionID, []byte(`{"code": "console.log(JSON.stringify(process.env))"}`))
		require.Equal(t, 0, reply.ExitCode, "stderr: %s", reply.Stderr)

		var env map[string]string
		require.NoErrorf(t, json.Unmarshal([]byte(reply.Stdout), &env), "decoding process.env dump %q", reply.Stdout)
		for k := range env {
			assert.Falsef(t, strings.HasPrefix(k, "FISSION_"), "exec'd subprocess env must never carry a FISSION_ var, found %q", k)
		}
	})
}

// hostnameContent is the turn payload both halves of
// TestAgentExecWorkspaceHydration decode from stdout: the exec'd code's own
// os.hostname() (never the pod's HOSTNAME env var, which ALLOWED_ENV_VARS
// deliberately excludes -- see interpreter.js.tmpl's trust-boundary comment)
// plus, on turn 2 only, the file content read back.
type hostnameContent struct {
	Hostname string `json:"hostname"`
	Content  string `json:"content"`
}

// TestAgentExecWorkspaceHydration exercises the PR-2 (abstraction C)
// hydrate/sync helper end to end: a file an exec turn writes into its
// scratch dir must survive the specialized pod being killed and a
// respecialize onto a DIFFERENT pod, because it was synced to the session's
// G12 workspace at turn 1's end and hydrated back down at turn 2's start --
// doc 14's PR-2 acceptance criterion (the doc-13 mcp-server-sandbox
// persistence demo pattern: write -> suspend-equivalent -> read back, hashes
// equal).
//
// The discriminating assertion is NOT merely "turn 2 reads the file" --
// os.hostname() must differ between the two turns, proving turn 2 actually
// ran on a new pod with no local disk continuity from turn 1, rather than a
// lingering Terminating pod quietly serving the request and passing this
// test for the wrong reason.
func TestAgentExecWorkspaceHydration(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	requireAgentRuntimeReachable(t, ctx, f)

	ns := f.NewTestNamespace(t)
	envName := "agent-workspace-hydrate-node-" + ns.ID
	fnName := "agent-workspace-hydrate-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	workdir := t.TempDir()
	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		ns.CLI(t, ctx, "agent", "create", "--name", fnName, "--env", envName, "--template", "interpreter", "--spec")
		ns.CLI(t, ctx, "spec", "apply")
	})

	base := f.AgentRuntimeBaseURL()
	turnURL := base + "/agents/" + ns.Name + "/" + fnName

	// Auth-posture probe -- same three-outcome rationale as
	// TestAgentRuntimeWorkspace's own probe (401 authz-enabled / 503
	// workspace disabled via STORAGESVC_URL unset / 404 pass-through). Run
	// BEFORE dispatching any turn: on a hand-managed install that doesn't
	// wire STORAGESVC_URL, hydrate/sync degrade gracefully turn-side (no
	// warnings surfaced as failures, just a no-op), so skipping here rather
	// than discovering it later as a confusing hostname-never-changed
	// failure is the honest signal.
	probeSessionID := "exec-workspace-posture-probe-" + ns.ID
	probeURL := base + "/workspace/" + ns.Name + "/" + fnName + "/" + probeSessionID
	probeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	require.NoErrorf(t, err, "building auth-posture probe request")
	probeResp, err := f.HTTPClient().Do(probeReq)
	require.NoErrorf(t, err, "GET %s (auth-posture probe)", probeURL)
	_ = probeResp.Body.Close()
	switch probeResp.StatusCode {
	case http.StatusUnauthorized:
		t.Skipf("agent runtime on this cluster requires a JWT (authz enabled) and this test framework has no "+
			"way to mint or sign one, so it cannot dispatch even the first turn here (GET %s got 401); skipping", probeURL)
	case http.StatusServiceUnavailable:
		t.Skipf("agent runtime's session workspace routes are disabled on this cluster (STORAGESVC_URL unset, "+
			"GET %s got 503); skipping", probeURL)
	case http.StatusNotFound:
		// Pass-through posture: proceed.
	default:
		t.Fatalf("auth-posture probe GET %s: want 401 (authz enabled), 404 (pass-through), or 503 "+
			"(workspace disabled), got %d", probeURL, probeResp.StatusCode)
	}

	const fileContent = "hydration-proof-payload"

	// Turn 1: write a file under a subdirectory (notes/summary.txt, not the
	// scratch dir root) and report which pod ran it. No session header --
	// mints the session, mirroring TestAgentExec's own first turn.
	writeCode := fmt.Sprintf(`
const fs = require('fs');
const os = require('os');
fs.mkdirSync('notes', { recursive: true });
fs.writeFileSync('notes/summary.txt', %q);
console.log(JSON.stringify({ hostname: os.hostname() }));
`, fileContent)
	writePayload, err := json.Marshal(map[string]any{"code": writeCode})
	require.NoError(t, err)

	writeReply, sessionID := dispatchExecTurn(t, ctx, f, turnURL, "", writePayload)
	require.NotEmpty(t, sessionID, "scaffolded interpreter never minted a session id")
	require.Equalf(t, 0, writeReply.ExitCode, "turn 1 exec failed: stderr=%s", writeReply.Stderr)
	assert.Emptyf(t, writeReply.WorkspaceWarnings, "turn 1's sync must not warn: %v", writeReply.WorkspaceWarnings)

	var before hostnameContent
	require.NoErrorf(t, json.Unmarshal([]byte(writeReply.Stdout), &before), "decoding turn 1 stdout %q", writeReply.Stdout)
	require.NotEmpty(t, before.Hostname, "turn 1 did not report a hostname")

	// Kill the specialized pod. This is what makes the next turn's pod
	// GENUINELY new: poolmgr respecializes a fresh pod, with an empty
	// $TMPDIR/exec/<session> -- the only way turn 2 can read
	// notes/summary.txt back is if it was hydrated down from the session
	// workspace turn 1 synced it to.
	podsBefore, err := ns.SpecializedFunctionPods(ctx, fnName)
	require.NoError(t, err)
	require.NotEmptyf(t, podsBefore, "turn 1 must have specialized a pod for %q", fnName)
	killedName, killedUID := podsBefore[0].Name, podsBefore[0].UID
	require.NoErrorf(t, f.KubeClient().CoreV1().Pods(ns.Name).Delete(ctx, killedName, metav1.DeleteOptions{}),
		"delete specialized pod %q", killedName)

	// Wait for the killed pod's UID to actually be gone before dispatching
	// turn 2 -- a still-Terminating pod could otherwise serve turn 2 and
	// mask a hydration bug behind a false "different hostname never even
	// got exercised" pass.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		cur, lerr := ns.SpecializedFunctionPods(ctx, fnName)
		if !assert.NoError(c, lerr) {
			return
		}
		for _, p := range cur {
			assert.NotEqualf(c, killedUID, p.UID, "killed pod %q must be fully gone before turn 2", killedName)
		}
	}, 2*time.Minute, 2*time.Second)

	// Turn 2, same session: read the file back and report which pod ran it.
	// Re-issuing the WHOLE turn on every retry (not polling a read-only
	// endpoint) mirrors TestAgentRuntimeWorkspace's own artifact-turn loop --
	// the respecialize + hydrate window can need more than one attempt.
	readCode := `
const fs = require('fs');
const os = require('os');
let data;
try {
  data = fs.readFileSync('notes/summary.txt', 'utf8');
} catch (err) {
  data = 'MISSING: ' + err.message;
}
console.log(JSON.stringify({ hostname: os.hostname(), content: data }));
`
	readPayload, err := json.Marshal(map[string]any{"code": readCode})
	require.NoError(t, err)

	var after hostnameContent
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		attemptCtx, cancel := context.WithTimeout(ctx, turnAttemptTimeout)
		defer cancel()
		resp, err := postExecTurn(attemptCtx, f, turnURL, sessionID, readPayload)
		if !assert.NoErrorf(c, err, "POST %s (turn 2)", turnURL) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equalf(c, http.StatusOK, resp.StatusCode, "turn 2 to %s", turnURL) {
			return
		}
		body, err := io.ReadAll(resp.Body)
		if !assert.NoErrorf(c, err, "reading turn 2 response body") {
			return
		}
		var reply execTurnReply
		if !assert.NoErrorf(c, json.Unmarshal(body, &reply), "decoding turn 2 reply %q", body) {
			return
		}
		if !assert.Equalf(c, 0, reply.ExitCode, "turn 2 exec failed: stderr=%s", reply.Stderr) {
			return
		}
		var info hostnameContent
		if !assert.NoErrorf(c, json.Unmarshal([]byte(reply.Stdout), &info), "decoding turn 2 stdout %q", reply.Stdout) {
			return
		}
		// The discriminating assertion: a genuinely NEW pod (different
		// hostname) that still reads back the ORIGINAL content proves
		// hydration ran -- not merely that some pod happened to still have
		// the file on local disk.
		if !assert.NotEqualf(c, before.Hostname, info.Hostname,
			"turn 2 must run on a DIFFERENT pod than turn 1 (both report hostname %q)", info.Hostname) {
			return
		}
		if !assert.Equalf(c, fileContent, info.Content, "hydrated file content mismatch (got %q)", info.Content) {
			return
		}
		after = info
	}, 3*time.Minute, 2*time.Second)
	require.NotEmpty(t, after.Hostname, "turn 2 never succeeded on a different pod")

	// doc 14's PR-2 acceptance criterion, verbatim: "hashes equal".
	assert.Equal(t, sha256Hex(fileContent), sha256Hex(after.Content),
		"hydrated content's sha256 must match what turn 1 wrote")
}
