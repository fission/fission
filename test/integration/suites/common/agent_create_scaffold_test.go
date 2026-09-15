// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/agentruntime"
	"github.com/fission/fission/test/integration/framework"
)

// TestAgentCreateScaffold exercises G17's in-repo DX half end to end:
// `fission agent create --spec` followed by `fission spec apply` must
// produce a WORKING stateful agent, not just CR objects that happen to
// decode. It dispatches one real turn against the scaffolded handler and
// checks the echoed reply against the exact JSON contract Task 1's node
// template implements (pkg/fission-cli/cmd/agent/templates/agent.js.tmpl):
// {"session": <id>, "turn": <n>, "state": {...}}, plus the default
// X-Fission-Agent-Yield: waiting the template always sets.
//
// Follows the proven spec-integration pattern (TestSpecMultifile,
// spec_multifile_test.go): the CLI's spec subcommands resolve ./specs (and
// any --deploy/--code glob) relative to cwd, with no --specdir override, so
// the whole spec sequence runs inside ns.WithCWD.
//
// The node Environment is created directly via the framework's CreateEnv
// helper -- the same pattern every other test in agentruntime_test.go uses
// for `--agent`-enabled functions -- rather than through a spec file.
// `agent create --spec`'s checkEnvironment only consults the spec SET
// (spec.ReadSpecs + FissionResources.ExistsInSpecs) for its "environment
// not found" warning, and spec.go's own Validate has a standing TODO for
// functions -> environments dangling refs, so an Environment that exists on
// the cluster but not in this test's spec set is sufficient for both `spec
// apply` and the scaffolded pod's specialization to succeed; `agent create`
// just prints an inaccurate-but-harmless "environment not found" next-steps
// line in that case, which this test does not assert on (create_test.go's
// golden tests own that wording).
//
// State is on by default for `agent create` (command.go's
// agentCreateStateFlag), so the scaffolded Function carries Spec.State --
// but this needs no functionState/statesvc setup on the cluster: the node
// template's loadState/saveState fall back to an in-memory map whenever
// FISSION_STATE_URL/FISSION_STATE_TOKEN_PATH are absent (which they are
// unless STATESVC_URL is set on the executor -- util.StateAPIEnvVars
// returns nil and injects nothing, keeping the pod byte-identical to a
// stateless one), so the turn below succeeds regardless of whether
// statesvc is deployed on this install.
func TestAgentCreateScaffold(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	requireAgentRuntimeReachable(t, ctx, f)

	ns := f.NewTestNamespace(t)
	envName := "agent-scaffold-node-" + ns.ID
	fnName := "agent-scaffold-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	workdir := t.TempDir()

	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		ns.CLI(t, ctx, "agent", "create", "--name", fnName, "--env", envName, "--spec")
		ns.CLI(t, ctx, "spec", "apply")
	})

	turnURL := f.AgentRuntimeBaseURL() + "/agents/" + ns.Name + "/" + fnName

	// One turn: no session header. The dispatcher mints a session id (the
	// scaffolded handler never mints its own), and by the time this returns
	// 200 the agentruntime replica's reconciler has also caught up with the
	// Function admission -- poll until 200 to absorb both that convergence
	// and the function's cold start, same shape as
	// TestAgentRuntimeSessionDispatch's first turn.
	var sessionID string
	var turnBody []byte
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
		assert.Equalf(c, agentruntime.YieldWaiting, resp.Header.Get(agentruntime.HeaderYield),
			"the scaffolded template always sets %s to the default %q", agentruntime.HeaderYield, agentruntime.YieldWaiting)
		body, err := io.ReadAll(resp.Body)
		if !assert.NoErrorf(c, err, "reading turn response body") {
			return
		}
		sessionID = sid
		turnBody = body
	}, 180*time.Second, 2*time.Second)
	require.NotEmpty(t, sessionID, "scaffolded agent never minted a session id")

	// The reply body itself is the DX proof: the scaffolded handler actually
	// ran (not just "the platform accepted the Function"), echoing the
	// dispatcher-minted session id and a turn count derived from the
	// X-Fission-Agent-Turns header (turns-before-this-call + 1 -- see
	// agent.js.tmpl's module.exports).
	var reply struct {
		Session string `json:"session"`
		Turn    int    `json:"turn"`
	}
	require.NoErrorf(t, json.Unmarshal(turnBody, &reply), "decoding scaffolded agent's reply body %q", turnBody)
	assert.Equal(t, sessionID, reply.Session,
		"the scaffolded template must echo the dispatcher-minted session id in its own reply body")
	assert.Equal(t, 1, reply.Turn,
		"the scaffolded template's first turn must report turn:1 (0 turns-before-this-call + 1)")

	// `fission agent sessions list` renders its table via os.Stdout directly
	// (util.PrintObjects/PrintItems), not the cobra Out buffer ns.CLI
	// captures -- CLICaptureStdoutBestEffort is the framework's
	// error-returning variant for exactly that class of subcommand (see its
	// doc comment: "archive get-url" etc.). Wrapped in EventuallyWithT for
	// the same reason every registry read elsewhere in this suite is: the
	// CLI's `sessions list` opens its OWN port-forward
	// (pkg/fission-cli/util/portless.go), a separate dial from the turn
	// above, and on a multi-replica install (or a still-converging
	// reconciler) that dial can land on a replica whose AgentView has not
	// yet observed this session -- retrying absorbs that instead of a
	// single-shot read racing the reconciler.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		listOut, err := ns.CLICaptureStdoutBestEffort(t, ctx, "agent", "sessions", "list", "--name", fnName)
		if !assert.NoErrorf(c, err, "agent sessions list --name %s\n%s", fnName, listOut) {
			return
		}
		assert.Containsf(c, listOut, sessionID, "agent sessions list --name %s must show session %s", fnName, sessionID)
	}, 60*time.Second, 3*time.Second)
}
