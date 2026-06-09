// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestStreamingSurvivesFunctionTimeout exercises the RFC-0008 streaming path
// end-to-end with a Node.js function that delays ~4s before responding.
//
//   - With `--streaming` and a short --fntimeout (2s), the router must NOT cut
//     the slow response: streaming drops the wall-clock function-timeout, so the
//     function completes and the client gets its body.
//   - The matched classic (non-streaming) function with the same 2s --fntimeout
//     is cut at the timeout (HTTP 504), proving the timeout is real and that
//     streaming is what escapes it.
//
// (True incremental flush — chunks arriving over time — is covered by the
// router unit tests; the Node env returns a single body, so this e2e focuses on
// the headline timeout-escape guarantee with a real runtime.)
func TestStreamingSurvivesFunctionTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-stream-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/streaming/delayed.js")

	t.Run("streaming function completes past the function timeout", func(t *testing.T) {
		fnName := "stream-delay-" + ns.ID
		routePath := "/" + fnName

		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name:      fnName,
			Env:       envName,
			Code:      codePath,
			FnTimeout: 2, // shorter than the function's ~4s delay
			Streaming: true,
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
		ns.WaitForFunction(t, ctx, fnName)

		body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("streamed-after-delay"))
		require.Contains(t, body, "streamed-after-delay",
			"streaming response must complete even though it runs past --fntimeout")
	})

	t.Run("classic function is cut at the function timeout", func(t *testing.T) {
		fnName := "classic-delay-" + ns.ID
		routePath := "/" + fnName

		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name:      fnName,
			Env:       envName,
			Code:      codePath,
			FnTimeout: 2, // shorter than the ~4s delay, and NOT streaming
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
		ns.WaitForFunction(t, ctx, fnName)

		// The router gives up at --fntimeout before the function responds.
		// Per TestFunctionTimeout, the cut surfaces as a 5xx (not strictly 504,
		// since the runtime may abort first). Poll until we observe a 5xx.
		f.Router(t).GetEventually(t, ctx, routePath, func(status int, _ string) bool {
			return status >= 500 && status < 600
		})
	})
}
