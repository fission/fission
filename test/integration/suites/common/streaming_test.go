// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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
			Code:              framework.WriteTestData(t, "nodejs/streaming/chat.js"),
			Streaming:         true,
			StreamingProtocol: "websocket",
		})

		// /fission-function/<fn> lives on the internal listener (post
		// GHSA-3g33-6vg6-27m8); dial it and sign the upgrade with
		// ServiceRouterInternal. http://127.0.0.1:8889 -> ws://127.0.0.1:8889.
		base := f.RouterInternalBaseURL()
		require.True(t, strings.HasPrefix(base, "http://"), "internal base must be http:// for ws rewrite, got %q", base)
		path := "/fission-function/" + fnName
		wsURL := "ws://" + strings.TrimPrefix(base, "http://") + path
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

		// The first connect can race pod warmup (the ws-handler may not be
		// attached yet, so the first frame is the router's "Error" placeholder).
		// Retry the whole connect + first-turn cycle until turn 1 echoes cleanly,
		// then keep that connection for the remaining turns.
		var conn *websocket.Conn
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
			defer dcancel()
			cn, _, err := websocket.DefaultDialer.DialContext(dctx, wsURL, signedHeader())
			if !assert.NoErrorf(c, err, "websocket dial %q", wsURL) {
				return
			}
			if err := cn.SetReadDeadline(time.Now().Add(10 * time.Second)); !assert.NoError(c, err) {
				_ = cn.Close()
				return
			}
			if err := cn.WriteMessage(websocket.TextMessage, []byte("hello")); !assert.NoError(c, err) {
				_ = cn.Close()
				return
			}
			_, msg, err := cn.ReadMessage()
			if !assert.NoError(c, err) {
				_ = cn.Close()
				return
			}
			if !assert.Equalf(c, "turn 1: hello", string(msg), "first chat turn (got %q)", string(msg)) {
				_ = cn.Close()
				return
			}
			conn = cn
		}, 90*time.Second, 2*time.Second)
		require.NotNil(t, conn, "websocket chat never established")
		defer func() { _ = conn.Close() }()

		// Multi-turn: the same socket must stay open across turns (the router
		// holds the pod for the conversation, not just the first frame). The
		// turn counter is per-connection state in chat.js, so it keeps counting.
		for turn := 2; turn <= 4; turn++ {
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
			send := "msg-" + strconv.Itoa(turn)
			require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(send)))
			_, msg, err := conn.ReadMessage()
			require.NoErrorf(t, err, "read turn %d", turn)
			require.Equalf(t, "turn "+strconv.Itoa(turn)+": "+send, string(msg),
				"chat turn %d must echo with its turn number over the same socket", turn)
		}

		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	})
}
