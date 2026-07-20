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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	env := "nodejs-state-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: env, Image: runtime})

	t.Run("counter_no_lost_updates", func(t *testing.T) {
		fnName := "fn-state-counter-" + ns.ID
		codePath := framework.WriteTestData(t, "nodejs/state/counter.js")
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: env, Code: codePath, State: true})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

		// Warm-up request is increment #1 (the get->CAS loop inside the
		// function retries lost races, so every successful request is
		// exactly one increment).
		body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("1"))
		require.Equal(t, "1", body)

		const extra = 19
		errs := make(chan error, extra)
		for range extra {
			go func() {
				status, body, err := f.Router(t).Get(ctx, "/"+fnName)
				if err == nil && status != http.StatusOK {
					err = fmt.Errorf("status %d: %s", status, body)
				}
				errs <- err
			}()
		}
		for range extra {
			require.NoError(t, <-errs)
		}

		// One more increment observes the exact total: 1 + 19 + 1.
		status, body, err := f.Router(t).Get(ctx, "/"+fnName)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, strconv.Itoa(extra+2), body, "S2: lost updates detected")
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

	t.Run("finalizer_purges_keyspace", func(t *testing.T) {
		client := stateAdminClient(t, f)
		secret := f.InternalAuthSecret()
		fnName := "fn-state-purge-" + ns.ID
		keyspace := "purge-me-" + strings.ToLower(ns.ID)
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: env, Code: framework.WriteTestData(t, "nodejs/state/counter.js"),
			State: true, StateKeyspace: keyspace,
		})
		cliEnv := map[string]string{"FISSION_INTERNAL_AUTH_SECRET": string(secret)}
		ns.CLIWithEnv(t, ctx, cliEnv, "fn", "state", "set", "--name", fnName, "--key", "doomed", "--value", "x")
		require.NotEmpty(t, adminListKeys(t, ctx, f, client, ns.Name, keyspace), "precondition: keyspace has data")

		ns.CLI(t, ctx, "fn", "delete", "--name", fnName)

		require.Eventually(t, func() bool {
			return len(adminListKeys(t, ctx, f, client, ns.Name, keyspace)) == 0
		}, 60*time.Second, 2*time.Second, "state-cleanup finalizer must purge the keyspace")
	})
}
