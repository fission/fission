// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/agentruntime"
	"github.com/fission/fission/test/integration/framework"
)

// execTurnReply is the exec verb's response contract:
// {"stdout": "...", "stderr": "...", "exitCode": 0, "durationMs": 12,
// "truncated": false}. This has no shared Go struct on either side ("zero
// new platform Go" is the point of a template-only exec verb) -- it exists
// only to decode the wire response in this test.
type execTurnReply struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	Truncated  bool   `json:"truncated"`
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
