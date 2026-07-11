// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/test/integration/framework"
)

// TestStreamingProtocols exercises the RFC-0008 streaming path end-to-end across
// its protocol options against a real Node.js runtime:
//
//   - auto / sse: a function that delays ~4s before responding. With --streaming
//     and a short 2s --fntimeout, the slow response completes (streaming drops the
//     wall-clock function timeout). The matched classic function is cut (5xx).
//   - websocket: a multi-turn chat over one long-lived socket — the router must
//     hold the pod for the whole conversation (the env-agnostic keepalive), not
//     just the first frame.
//
// (True incremental SSE flush — chunks arriving over time — is covered by the
// router unit tests; the Node env returns a single body, so the http legs here
// focus on the headline timeout-escape guarantee with a real runtime.)
func TestStreamingProtocols(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-stream-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	delayCode := framework.WriteTestData(t, "nodejs/streaming/delayed.js")

	// httpStreamingCompletes deploys the delay function with --streaming and the
	// given protocol, then asserts it completes past the 2s --fntimeout.
	httpStreamingCompletes := func(t *testing.T, protocol string) {
		fnName := "stream-" + protocol + "-" + ns.ID
		routePath := "/" + fnName
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name:              fnName,
			Env:               envName,
			Code:              delayCode,
			FnTimeout:         2, // shorter than the function's ~4s delay
			Streaming:         true,
			StreamingProtocol: protocol,
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
		ns.WaitForFunction(t, ctx, fnName)

		body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("streamed-after-delay"))
		require.Contains(t, body, "streamed-after-delay",
			"streaming (%s) response must complete even though it runs past --fntimeout", protocol)
	}

	t.Run("auto protocol completes past the function timeout", func(t *testing.T) {
		httpStreamingCompletes(t, "auto")
	})

	t.Run("sse protocol completes past the function timeout", func(t *testing.T) {
		httpStreamingCompletes(t, "sse")
	})

	t.Run("classic (non-streaming) is cut at the function timeout", func(t *testing.T) {
		fnName := "classic-" + ns.ID
		routePath := "/" + fnName
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnName, Env: envName, Code: delayCode, FnTimeout: 2, // NOT streaming
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
		ns.WaitForFunction(t, ctx, fnName)

		// Per TestFunctionTimeout, the cut surfaces as a 5xx (the runtime may abort
		// before the router's 504). Poll until we observe a 5xx.
		f.Router(t).GetEventually(t, ctx, routePath, func(status int, _ string) bool {
			return status >= 500 && status < 600
		})
	})

	t.Run("websocket multi-turn chat", func(t *testing.T) {
		fnName := "ws-chat-" + ns.ID
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name:              fnName,
			Env:               envName,
			Code:              framework.WriteTestData(t, "nodejs/websocket/broadcast.js"),
			Streaming:         true,
			StreamingProtocol: "websocket",
		})

		// /fission-function/<fn> lives on the internal listener (post
		// GHSA-3g33-6vg6-27m8); dial it and sign the upgrade with
		// ServiceRouterInternal. The ws host is a portless route name,
		// resolved by the framework's HTTP client during the handshake.
		path := "/fission-function/" + fnName
		wsURL := f.RouterInternalWSURL(path)
		master := f.InternalAuthSecret()

		signedHeader := func() http.Header {
			hdr := http.Header{}
			if len(master) > 0 {
				key := hmacauth.DeriveServiceKey(master, hmacauth.ServiceRouterInternal)
				ts := time.Now().Unix()
				sig := hmacauth.Sign(key, http.MethodGet, path, nil, ts)
				hdr.Set(hmacauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
				hdr.Set(hmacauth.HeaderSignature, sig)
			}
			return hdr
		}

		// Drive a multi-turn conversation over one long-lived socket: broadcast.js
		// echoes each frame back to the sender, so three distinct messages must
		// come back in order. The socket staying open across all turns is the
		// point — the router holds the pod for the whole conversation, not just
		// the first frame. The pod can be warming on the first connect (the env's
		// first frame is then the "Error" placeholder), so run the whole
		// conversation inside a retry on a fresh socket and skip "Error" frames.
		turns := []string{"turn-one", "turn-two", "turn-three"}
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
			defer dcancel()
			conn, _, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{HTTPClient: f.HTTPClient(), HTTPHeader: signedHeader()})
			if !assert.NoErrorf(c, err, "websocket dial %q", wsURL) {
				return
			}
			defer func() { _ = conn.CloseNow() }()

			for _, msg := range turns {
				// coder/websocket carries the per-turn read deadline on the context.
				ioCtx, iocancel := context.WithTimeout(ctx, 10*time.Second)
				if err := conn.Write(ioCtx, websocket.MessageText, []byte(msg)); !assert.NoErrorf(c, err, "write %q", msg) {
					iocancel()
					return
				}
				// Skip any "Error" warmup-placeholder frames the env may emit
				// before the real echo.
				var got string
				for {
					_, m, rerr := conn.Read(ioCtx)
					if !assert.NoErrorf(c, rerr, "read echo for %q", msg) {
						iocancel()
						return
					}
					if string(m) == "Error" {
						continue
					}
					got = string(m)
					break
				}
				iocancel()
				if !assert.Equalf(c, msg, got, "multi-turn echo for %q over the same socket", msg) {
					return
				}
			}
		}, 120*time.Second, 3*time.Second)
	})
}
