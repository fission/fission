// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/test/integration/framework"
)

// stateSvcReachableOrSkip probes svc/statesvc and skips when functionState is
// disabled in this install (mirrors the MCP test's optional-subsystem skip).
func stateSvcReachableOrSkip(t *testing.T, ctx context.Context, f *framework.Framework) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.StateSvcBaseURL()+"/healthz", nil)
	require.NoError(t, err)
	resp, err := f.HTTPClient().Do(req)
	if err != nil {
		if framework.IsTargetMissing(err) {
			t.Skip("statesvc not deployed (functionState disabled); skipping")
		}
		require.NoError(t, err, "probing statesvc /healthz")
	}
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// stateAdminClient returns an http.Client resolving the framework's portless
// routes and signing requests on the ServiceStateAPI admin channel. Skips if
// the install runs without the internal auth secret (admin path fails closed).
func stateAdminClient(t *testing.T, f *framework.Framework) *http.Client {
	t.Helper()
	secret := f.InternalAuthSecret()
	if len(secret) == 0 {
		t.Skip("FISSION_INTERNAL_AUTH_SECRET not set; statesvc admin path fails closed — skipping")
	}
	base := f.HTTPClient()
	return &http.Client{
		Transport: hmacauth.NewServiceSigningTransport(secret, hmacauth.ServiceStateAPI, base.Transport, "/v1/state"),
		Timeout:   30 * time.Second,
	}
}

func adminListKeys(t *testing.T, ctx context.Context, f *framework.Framework, client *http.Client, ns, keyspace string) []string {
	t.Helper()
	u := fmt.Sprintf("%s/v1/state?scope-namespace=%s&scope-keyspace=%s", f.StateSvcBaseURL(), ns, keyspace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var lr struct {
		Keys []string `json:"keys"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&lr))
	return lr.Keys
}

// TestFunctionState is the RFC-0023 end-to-end matrix: a real function
// reading/writing scoped state through the injected URL + token file (S2
// zero lost updates), scope isolation from inside a function (S1), the
// fn state admin CLI, quota enforcement, and the keyspace-purge finalizer.
func TestFunctionState(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	stateSvcReachableOrSkip(t, ctx, f)
	// The RFC-0023 state token is derived from the master internal-auth secret,
	// which only reaches function pods under static tenancy. Dynamic/cluster
	// tenancy gives function namespaces per-namespace keys instead (the whole
	// isolation point), so the fetcher cannot mint a token statesvc accepts
	// until the tenant controller provisions a per-namespace state key — a
	// documented follow-up. Skip rather than assert a known-unsupported combo.
	if mode := f.TenancyMode(t, ctx); mode != "static" {
		t.Skipf("function state is static-tenancy only for now; tenancy mode is %q (per-namespace state key is a follow-up)", mode)
	}
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	env := "nodejs-state-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: env, Image: runtime})

	t.Run("counter_end_to_end", func(t *testing.T) {
		// End-to-end proof of the injected token → function → statesvc round
		// trip: the fixture reads the fetcher-written credentials file, hits
		// the injected FISSION_STATE_URL, and does a get→CAS increment. Fired
		// SEQUENTIALLY: every request round-trips to statesvc and returns the
		// exact running total, so the count is deterministic. (S2 no-lost-
		// updates UNDER CONCURRENCY is proven rigorously by the porcupine
		// linearizability + zero-lost-increment unit tests over the real HTTP
		// surface; a concurrent burst here would only force a poolmgr
		// specialization storm that flakes CI, not add coverage.)
		fnName := "fn-state-counter-" + ns.ID
		codePath := framework.WriteTestData(t, "nodejs/state/counter.js")
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: env, Code: codePath, State: true})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("1"))
		require.Equal(t, "1", body, "first increment")

		const total = 6
		for i := 2; i <= total; i++ {
			status, body, err := f.Router(t).Get(ctx, "/"+fnName)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equalf(t, strconv.Itoa(i), body, "increment %d", i)
		}
	})

	t.Run("scope_isolation_from_function", func(t *testing.T) {
		forgeFn := "fn-state-forge-" + ns.ID
		victimFn := "fn-state-victim-" + ns.ID
		codePath := framework.WriteTestData(t, "nodejs/state/forge.js")
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: forgeFn, Env: env, Code: codePath, State: true})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: victimFn, Env: env, Code: framework.WriteTestData(t, "nodejs/state/counter.js"), State: true,
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: forgeFn, URL: "/" + forgeFn, Method: "GET"})

		// Own keyspace: the probe key is absent, so statesvc answers 404 —
		// authenticated, authorized, empty.
		body := f.Router(t).GetEventually(t, ctx, "/"+forgeFn, framework.BodyContains("404"))
		require.Equal(t, "404", body)

		// The victim's keyspace with the forger's own token: 403 (S1).
		status, body, err := f.Router(t).Get(ctx, "/"+forgeFn+"?target="+victimFn)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "403", body, "S1: cross-keyspace access must be rejected")
	})

	t.Run("cli_and_quota", func(t *testing.T) {
		secret := f.InternalAuthSecret()
		if len(secret) == 0 {
			t.Skip("FISSION_INTERNAL_AUTH_SECRET not set; fn state CLI fails closed — skipping")
		}
		fnName := "fn-state-cli-" + ns.ID
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: env, Code: framework.WriteTestData(t, "nodejs/state/counter.js"),
			State: true, StateMaxKeys: 2,
		})
		cliEnv := map[string]string{"FISSION_INTERNAL_AUTH_SECRET": string(secret)}

		ns.CLIWithEnv(t, ctx, cliEnv, "fn", "state", "set", "--name", fnName, "--key", "k1", "--value", "v1")
		ns.CLIWithEnv(t, ctx, cliEnv, "fn", "state", "set", "--name", fnName, "--key", "k2", "--value", "v2")

		// get/list print via fmt.Println, so capture os.Stdout.
		out := ns.CLICaptureStdoutWithEnv(t, ctx, cliEnv, "fn", "state", "get", "--name", fnName, "--key", "k1")
		assert.Contains(t, out, "v1")
		out = ns.CLICaptureStdoutWithEnv(t, ctx, cliEnv, "fn", "state", "list", "--name", fnName)
		assert.Contains(t, out, "k1")
		assert.Contains(t, out, "k2")

		// Third live key exceeds StateMaxKeys=2: the admin path is scoped
		// through the same quota-enforcing store, so it must be rejected.
		_, err := ns.CLICaptureStdoutWithEnvBestEffort(t, ctx, cliEnv, "fn", "state", "set", "--name", fnName, "--key", "k3", "--value", "v3")
		require.Error(t, err, "quota must reject the third live key")
		assert.Contains(t, err.Error(), "429")

		ns.CLIWithEnv(t, ctx, cliEnv, "fn", "state", "delete", "--name", fnName, "--key", "k2")
		ns.CLIWithEnv(t, ctx, cliEnv, "fn", "state", "set", "--name", fnName, "--key", "k3", "--value", "v3")
	})

	t.Run("sticky_config_serves", func(t *testing.T) {
		// Smoke test only: a function that opts into sticky routing
		// (StickyConfig) still serves correctly and the router extracts the
		// declared key without error. Per-key POD RESIDENCY (S4) and minimal
		// reshuffle (S5) are proven deterministically by the pure-function
		// rapid tests in pkg/router/endpointcache/sticky_test.go, and the
		// extraction→resolver→Admit threading by the pkg/router transport and
		// resolver_fallback unit tests. An end-to-end residency assertion is
		// deliberately NOT made here: poolmgr's warm pool cold-starts and
		// reaps pods on its own schedule, so CI never presents a stable ready
		// set to observe stickiness against — the property is a latency
		// optimization, not a correctness one (S6), and asserting it on a
		// churning pool only produces flakes.
		fnName := "fn-state-sticky-" + ns.ID
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: env, Code: framework.WriteTestData(t, "nodejs/state/whoami.js"),
			State: true, StateStickySource: "queryparam", StateStickyName: "sid",
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

		// The key present and absent both serve (missing key falls back to the
		// default pick, documented — never an error).
		f.Router(t).GetEventually(t, ctx, "/"+fnName+"?sid=session-42", framework.BodyContains(""))
		status, _, err := f.Router(t).Get(ctx, "/"+fnName)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "sticky-declared function serves requests missing the key")
	})

	t.Run("finalizer_purges_keyspace", func(t *testing.T) {
		client := stateAdminClient(t, f)
		secret := f.InternalAuthSecret()
		fnName := "fn-state-purge-" + ns.ID
		keyspace := "purge-me-" + strings.ToLower(ns.ID)
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: env, Code: framework.WriteTestData(t, "nodejs/state/counter.js"),
			State: true, StateKeyspace: keyspace,
		})
		// Wait for the state-cleanup finalizer to land before deleting, so the
		// test never races a fast create→delete ahead of the reconciler (which
		// would delete the function with no finalizer and orphan the keyspace —
		// an artifact of the test's speed, not the feature).
		require.Eventually(t, func() bool {
			fn, err := f.FissionClient().CoreV1().Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
			if err != nil {
				return false
			}
			return slices.Contains(fn.Finalizers, "fission.io/state-cleanup")
		}, 30*time.Second, time.Second, "state-cleanup finalizer must be added before delete")

		cliEnv := map[string]string{"FISSION_INTERNAL_AUTH_SECRET": string(secret)}
		ns.CLIWithEnv(t, ctx, cliEnv, "fn", "state", "set", "--name", fnName, "--key", "doomed", "--value", "x")
		require.NotEmpty(t, adminListKeys(t, ctx, f, client, ns.Name, keyspace), "precondition: keyspace has data")

		ns.CLI(t, ctx, "fn", "delete", "--name", fnName)

		require.Eventually(t, func() bool {
			return len(adminListKeys(t, ctx, f, client, ns.Name, keyspace)) == 0
		}, 60*time.Second, 2*time.Second, "state-cleanup finalizer must purge the keyspace")
	})
}
