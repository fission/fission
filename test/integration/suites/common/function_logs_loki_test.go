// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionLogsLokiCorrelation exercises the RFC-0016 OTLP logging read path
// end to end against a real Loki fed by an OpenTelemetry Collector (the CI-only
// stack in test/integration/otel/, which Fission does NOT bundle in its chart).
//
// It invokes a function with a known X-Fission-Request-ID, then asserts
// `fission function logs --dbtype loki --request-id <id>` returns the router
// access record for exactly that invocation. That proves the whole correlation
// pipeline: the router emits the structured access record (RFC-0016 section 1a,
// router.displayAccessLog on in kind-ci) → the collector tails the router's
// stdout, keeps the record, and pushes it to Loki with the function identity as
// resource attributes → Loki indexes fission_function_uid/namespace as labels →
// the loki logdb driver's LogQL ({fission_function_uid="<uid>"} | json |
// fission_request_id="<id>") selects the line.
//
// Gated on FISSION_TEST_LOKI: the stack is stood up only on the CI leg that
// installs it (see .github/workflows/push_pr.yaml) with LOKI_URL pointing at the
// port-forwarded Loki. The test skips everywhere else, mirroring how the other
// infra-gated tests (Gateway API, OCI registry) skip when their dependency is
// absent.
func TestFunctionLogsLokiCorrelation(t *testing.T) {
	t.Parallel()

	if os.Getenv("FISSION_TEST_LOKI") == "" {
		t.Skip("FISSION_TEST_LOKI unset; skipping Loki log-correlation test (needs the OTel Collector + Loki stack from test/integration/otel/, router.displayAccessLog=true, and LOKI_URL)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-loki-" + ns.ID
	fnName := "lokifn-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	// Make sure the route is live before the correlated invocation.
	r := f.Router(t)
	r.GetEventually(t, ctx, routePath, framework.BodyContains("hello"))

	// Invoke with a request id we control: RFC-0015's correlation middleware
	// honors an inbound X-Fission-Request-ID, so the access record — and thus
	// the Loki query — keys on this exact value.
	reqID := "itest-loki-" + ns.ID
	status, _, err := r.GetWithRequestID(ctx, routePath, reqID)
	require.NoError(t, err, "invoke with request id")
	require.Equal(t, 200, status, "correlated invocation should succeed")

	// Poll the loki driver until the collector has shipped the access record
	// and Loki has indexed it. An empty result is not an error (no record yet),
	// so the best-effort capture lets us retry; a real backend failure surfaces.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		out, qerr := ns.CLICaptureStdoutBestEffort(t, ctx,
			"function", "logs",
			"--name", fnName,
			"--dbtype", "loki",
			"--request-id", reqID)
		if !assert.NoErrorf(c, qerr, "loki query failed:\n%s", out) {
			return
		}
		assert.Containsf(c, out, reqID,
			"loki logs --request-id should return the access record carrying %q; got:\n%s", reqID, out)
		assert.Containsf(c, out, fnName,
			"the access record should name function %q; got:\n%s", fnName, out)
	}, 90*time.Second, 3*time.Second)
}
