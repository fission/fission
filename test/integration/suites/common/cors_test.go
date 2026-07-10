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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestCORS_RouterSecurity exercises the round-3 CORS / security-headers
// wrap end-to-end against a live router. No user function is needed —
// these probe router-owned routes (/_version, /router-healthz) and the
// internal listener, so the test is fast and runs in parallel with any
// other suite.
//
// Mitigations under test (see pkg/utils/httpsecurity):
//   - SecurityHeaders adds X-Content-Type-Options: nosniff and Vary:
//     Origin on every response on the public listener.
//   - DenyAllCORS rejects cross-origin preflights with 403 on router-
//     owned public routes AND on the internal listener (before HMAC).
func TestCORS_RouterSecurity(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	// Unsigned registry-backed client; request deadlines come from ctx.
	httpClient := f.HTTPClient()

	t.Run("router-owned routes carry nosniff and Vary: Origin", func(t *testing.T) {
		// /_version is the canonical router-owned route; SecurityHeaders
		// wraps the whole public mux so every response should carry
		// these headers regardless of the path's CORS posture.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Router(t).BaseURL()+"/_version", nil)
		require.NoError(t, err)
		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equalf(t, "nosniff", resp.Header.Get("X-Content-Type-Options"),
			"X-Content-Type-Options: nosniff missing on /_version")
		assert.Containsf(t, resp.Header.Get("Vary"), "Origin",
			"Vary: Origin missing on /_version (got Vary=%q)", resp.Header.Get("Vary"))
	})

	t.Run("router-owned routes reject cross-origin preflight with 403", func(t *testing.T) {
		// Each router-owned route registers OPTIONS alongside its real
		// verb so the preflight reaches DenyAllCORS (which 403s) rather
		// than gorilla/mux's method gate (which would 405).
		for _, path := range []string{"/_version", "/router-healthz"} {
			path := path
			t.Run(path, func(t *testing.T) {
				req, err := http.NewRequestWithContext(ctx, http.MethodOptions, f.Router(t).BaseURL()+path, nil)
				require.NoError(t, err)
				req.Header.Set("Origin", "https://attacker.example")
				req.Header.Set("Access-Control-Request-Method", "GET")
				resp, err := httpClient.Do(req)
				require.NoError(t, err)
				defer resp.Body.Close()

				assert.Equalf(t, http.StatusForbidden, resp.StatusCode,
					"cross-origin preflight to %s must be 403 from DenyAllCORS, got %d", path, resp.StatusCode)
				assert.Emptyf(t, resp.Header.Get("Access-Control-Allow-Origin"),
					"DenyAllCORS must not echo Allow-Origin on rejected preflight (got %q)",
					resp.Header.Get("Access-Control-Allow-Origin"))
				// SecurityHeaders runs outermost — even the 403 must
				// carry the security headers.
				assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"),
					"403 response must still carry X-Content-Type-Options: nosniff")
			})
		}
	})

	t.Run("internal listener rejects cross-origin preflight before HMAC", func(t *testing.T) {
		// The internal listener (port 8889 via routerInternal) hosts
		// /fission-function/<ns>/<name>. DenyAllCORS wraps the verifier
		// so a browser-driven preflight 403s before HMAC even buffers
		// the body. We intentionally do NOT sign the request — if
		// DenyAllCORS is bypassed by a regression, HMAC would surface
		// 401 (when configured) instead of the desired 403.
		req, err := http.NewRequestWithContext(ctx, http.MethodOptions,
			f.RouterInternalBaseURL()+"/fission-function/default/never-exists", nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://attacker.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equalf(t, http.StatusForbidden, resp.StatusCode,
			"internal listener must 403 cross-origin preflight before HMAC, got %d", resp.StatusCode)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"),
			"internal-listener 403 must still carry X-Content-Type-Options: nosniff")
	})
}

// TestCORS_HTTPTrigger exercises the per-HTTPTrigger CorsConfig CRD field
// added in round-3. It creates one Node.js function and reuses it across
// three subtests:
//
//   - default (no CorsConfig) returns no Access-Control-* headers,
//     even when called cross-origin (browser SOP enforces deny).
//   - allowlist (CorsConfig set) echoes the configured origin on
//     preflight + actual request, and 403s mismatched preflights.
//   - admission (CorsConfig invalid) is rejected at admission so a
//     broken trigger never reconciles.
func TestCORS_HTTPTrigger(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	httpClient := f.HTTPClient()

	ns := f.NewTestNamespace(t)
	envName := "nodejs-cors-" + ns.ID
	fnName := "fn-cors-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName,
		Env:  envName,
		Code: codePath,
	})

	t.Run("default trigger denies CORS by SOP", func(t *testing.T) {
		// CLI doesn't yet expose CorsConfig, so a CLI-created route
		// reflects the deny-by-default contract for triggers without
		// CorsConfig.
		routePath := "/cors-default-" + ns.ID
		ns.CreateRoute(t, ctx, framework.RouteOptions{
			Name:     "route-default-" + ns.ID,
			Function: fnName,
			URL:      routePath,
			Method:   http.MethodGet,
		})
		// Wait for the trigger to be reachable before probing CORS
		// behaviour — otherwise a 404 from the mux would mask the
		// header assertion.
		f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Router(t).BaseURL()+routePath, nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://attacker.example")
		resp, err := httpClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"),
			"trigger without CorsConfig must not echo Allow-Origin")
		// SecurityHeaders still applies to the user-trigger response
		// proxied through the public listener.
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"),
			"user-trigger response should still carry nosniff via SecurityHeaders")
	})

	t.Run("trigger with CorsConfig allowlists configured origin", func(t *testing.T) {
		// CLI does not expose CorsConfig, so we go straight through the
		// typed Fission client. CreateRoute uses CLI; here we Create the
		// HTTPTrigger directly so the CorsConfig field is set on the
		// initial spec (avoiding a second update round-trip).
		routeName := "route-cors-allow-" + ns.ID
		routePath := "/cors-allow-" + ns.ID
		const allowedOrigin = "https://app.example.com"

		trigger := &fv1.HTTPTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: routeName, Namespace: ns.Name},
			Spec: fv1.HTTPTriggerSpec{
				RelativeURL: routePath,
				Methods:     []string{http.MethodGet},
				FunctionReference: fv1.FunctionReference{
					Type: fv1.FunctionReferenceTypeFunctionName,
					Name: fnName,
				},
				CorsConfig: &fv1.HTTPTriggerCorsConfig{
					AllowOrigins:  []string{allowedOrigin},
					AllowMethods:  []string{http.MethodGet},
					AllowHeaders:  []string{"X-Test-Header"},
					ExposeHeaders: []string{"X-Request-Id"},
					MaxAge:        "10m",
				},
			},
		}
		_, err := f.FissionClient().CoreV1().HTTPTriggers(ns.Name).Create(ctx, trigger, metav1.CreateOptions{})
		require.NoError(t, err, "create trigger with CorsConfig")
		t.Cleanup(func() {
			dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = f.FissionClient().CoreV1().HTTPTriggers(ns.Name).Delete(dctx, routeName, metav1.DeleteOptions{})
		})
		// Wait for the route to be reconciled into the router mux
		// (router.subscribeRouter watches HTTPTriggers and rebuilds on
		// each event; cold-start tail is a few seconds).
		f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))

		t.Run("preflight from allowed origin echoes Allow-Origin", func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, http.MethodOptions, f.Router(t).BaseURL()+routePath, nil)
			require.NoError(t, err)
			req.Header.Set("Origin", allowedOrigin)
			req.Header.Set("Access-Control-Request-Method", http.MethodGet)
			req.Header.Set("Access-Control-Request-Headers", "X-Test-Header")
			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equalf(t, http.StatusNoContent, resp.StatusCode,
				"matched preflight must be 204, got %d", resp.StatusCode)
			assert.Equal(t, allowedOrigin, resp.Header.Get("Access-Control-Allow-Origin"))
			assert.Equal(t, http.MethodGet, resp.Header.Get("Access-Control-Allow-Methods"))
			assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "X-Test-Header")
			assert.Equal(t, "600", resp.Header.Get("Access-Control-Max-Age"),
				"MaxAge: 10m must render as 600s")
		})

		t.Run("preflight from disallowed origin returns 403", func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, http.MethodOptions, f.Router(t).BaseURL()+routePath, nil)
			require.NoError(t, err)
			req.Header.Set("Origin", "https://attacker.example")
			req.Header.Set("Access-Control-Request-Method", http.MethodGet)
			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"mismatched preflight must be 403")
			assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
		})

		t.Run("actual GET from allowed origin echoes Allow-Origin and exposes headers", func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Router(t).BaseURL()+routePath, nil)
			require.NoError(t, err)
			req.Header.Set("Origin", allowedOrigin)
			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, allowedOrigin, resp.Header.Get("Access-Control-Allow-Origin"))
			assert.Contains(t, resp.Header.Get("Access-Control-Expose-Headers"), "X-Request-Id")
		})

		t.Run("actual GET from disallowed origin does not echo Allow-Origin", func(t *testing.T) {
			// SOP-enforced deny: handler still runs, response is returned,
			// but no Allow-Origin is echoed so the browser blocks the read.
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Router(t).BaseURL()+routePath, nil)
			require.NoError(t, err)
			req.Header.Set("Origin", "https://attacker.example")
			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode,
				"disallowed origin still gets the response body (handler runs); browser SOP blocks the read via missing Allow-Origin")
			assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"),
				"mismatched origin must not be echoed back")
		})
	})

	t.Run("admission rejects CorsConfig with wildcard + credentials", func(t *testing.T) {
		// The API server's CEL validation (x-kubernetes-validations) rejects
		// this combination at admission, so the trigger never reconciles into
		// a broken state.
		bad := &fv1.HTTPTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-cors-" + ns.ID, Namespace: ns.Name},
			Spec: fv1.HTTPTriggerSpec{
				RelativeURL: "/bad-cors-" + ns.ID,
				Methods:     []string{http.MethodGet},
				FunctionReference: fv1.FunctionReference{
					Type: fv1.FunctionReferenceTypeFunctionName,
					Name: fnName,
				},
				CorsConfig: &fv1.HTTPTriggerCorsConfig{
					AllowOrigins:     []string{"*"},
					AllowCredentials: true,
				},
			},
		}
		_, err := f.FissionClient().CoreV1().HTTPTriggers(ns.Name).Create(ctx, bad, metav1.CreateOptions{})
		require.Error(t, err, "API server should reject CorsConfig with wildcard + credentials")
		// The API server surfaces a 4xx Invalid from the CEL rule. Confirm it
		// isn't a NotFound (which would mean the CRD validation isn't installed)
		// and that the error names the offending field so future regressions
		// surface clearly.
		assert.Falsef(t, apierrors.IsNotFound(err), "expected validation rejection, got NotFound: %v", err)
		msg := strings.ToLower(err.Error())
		assert.Truef(t,
			strings.Contains(msg, "corsconfig") || strings.Contains(msg, "allowcredentials") || strings.Contains(msg, "credentials"),
			"rejection error should mention the CORS config / credentials (got %v)", err)
	})

	t.Run("router marks RouteAdmitted=False for CorsConfig with origin containing path", func(t *testing.T) {
		// Browsers match Allow-Origin against scheme + host[:port] only; an
		// origin with a path can never match. Detecting that needs url.Parse —
		// a Go parser CRD CEL cannot express — so the router admits the trigger
		// and reports RouteAdmitted=False instead of rejecting at admission
		// (the deleted httptrigger webhook used to reject it). The fission CLI
		// still rejects this client-side.
		bad := &fv1.HTTPTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-cors-path-" + ns.ID, Namespace: ns.Name},
			Spec: fv1.HTTPTriggerSpec{
				RelativeURL: "/bad-cors-path-" + ns.ID,
				Methods:     []string{http.MethodGet},
				FunctionReference: fv1.FunctionReference{
					Type: fv1.FunctionReferenceTypeFunctionName,
					Name: fnName,
				},
				CorsConfig: &fv1.HTTPTriggerCorsConfig{
					AllowOrigins: []string{"https://app.example.com/api"},
				},
			},
		}
		_, err := f.FissionClient().CoreV1().HTTPTriggers(ns.Name).Create(ctx, bad, metav1.CreateOptions{})
		require.NoError(t, err, "invalid CORS origin is now admitted (validation moved to the RouteAdmitted condition)")

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			conds := ns.GetHTTPTriggerConditions(t, ctx, bad.Name)
			cond := meta.FindStatusCondition(conds, fv1.HTTPTriggerConditionRouteAdmitted)
			if assert.NotNil(c, cond, "RouteAdmitted condition should be set") {
				assert.Equal(c, metav1.ConditionFalse, cond.Status)
				assert.Equal(c, fv1.HTTPTriggerReasonInvalidCorsConfig, cond.Reason)
			}
		}, 60*time.Second, time.Second, "router should mark RouteAdmitted=False for an invalid CORS origin")
	})
}
