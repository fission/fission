// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionLogsFollow exercises the RFC-0016 streaming `fission function logs
// --follow` against the default (kubernetes) driver: with --follow the driver
// follows the pod log stream (PodLogOptions.Follow) instead of the one-second
// poll. The function logs "log test" per invocation; after a few GETs we run
// `logs --follow` under a bounded context — the follow stream backfills the
// existing lines (TailLines) immediately, so the assertion does not depend on
// real-time new output, then the context cancel ends the stream cleanly.
func TestFunctionLogsFollow(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-follow-" + ns.ID
	fnName := "followtest-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/log/log.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	r := f.Router(t)
	r.GetEventually(t, ctx, routePath, framework.BodyContains("log test"))
	for i := 0; i < 3; i++ {
		status, _, err := r.Get(ctx, routePath)
		require.NoErrorf(t, err, "warm GET #%d", i)
		require.Equalf(t, 200, status, "warm GET #%d should be 200", i)
	}

	// Poll until the follow stream reports the backfilled lines. A bounded
	// sub-context ends the otherwise-blocking --follow; StreamLogs treats the
	// cancel as a clean stop.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		followCtx, followCancel := context.WithTimeout(ctx, 15*time.Second)
		defer followCancel()
		out := ns.CLICaptureStdout(t, followCtx, "function", "logs", "--name", fnName, "--follow")
		assert.GreaterOrEqualf(c, strings.Count(out, "log test"), 1,
			"follow stream should surface the function's log lines; got:\n%s", out)
	}, 90*time.Second, 5*time.Second)
}
