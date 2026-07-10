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

// TestWebsocket is the Go port of test/tests/websocket/test_ws.sh.
//
// Creates a Node.js function exporting a websocket-aware handler
// (broadcast.js: echoes received messages back to all connected clients,
// including the sender), then dials the router's well-known internal
// route ws://<router>/fission-function/<fn>, sends a single text frame,
// and asserts the same frame comes back.
func TestWebsocket(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-ws-" + ns.ID
	fnName := "ws-bs-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})
	codePath := framework.WriteTestData(t, "nodejs/websocket/broadcast.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
	})

	// /fission-function/<fn> moved off the public listener after
	// GHSA-3g33-6vg6-27m8 — dial the internal listener instead, and
	// sign the upgrade-handshake HTTP request with ServiceRouterInternal.
	// The ws host is a portless route name, resolved by the framework's
	// HTTP client during the upgrade handshake.
	path := "/fission-function/" + fnName
	wsURL := f.RouterInternalWSURL(path)

	// Build the HMAC-signed upgrade headers. Empty secret leaves the
	// dial unsigned, which is fine when internalAuth.enabled=false on
	// the cluster (verifier short-circuits to pass-through).
	master := f.InternalAuthSecret()
	dialHeader := http.Header{}
	if len(master) > 0 {
		key := hmacauth.DeriveServiceKey(master, hmacauth.ServiceRouterInternal)
		ts := time.Now().Unix()
		// gorilla/websocket dials with GET; canonical includes the
		// request-URI (path + raw query). No body on the upgrade
		// handshake, so body is nil.
		sig := hmacauth.Sign(key, http.MethodGet, path, nil, ts)
		dialHeader.Set(hmacauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
		dialHeader.Set(hmacauth.HeaderSignature, sig)
	}

	// First connect can race the executor specializing the pod; retry
	// the entire dial + send + receive cycle. On slower clusters
	// (k8s 1.32+ in CI) the websocket-upgrade handshake can succeed
	// before the function pod's ws-handler is fully attached, in which
	// case the first frame back is the router's "Error" placeholder
	// rather than broadcast.js's echo. Re-establishing the connection
	// drives the retry through pod warmup.
	var conn *websocket.Conn
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		hdr := dialHeader
		if len(master) > 0 {
			key := hmacauth.DeriveServiceKey(master, hmacauth.ServiceRouterInternal)
			ts := time.Now().Unix()
			sig := hmacauth.Sign(key, http.MethodGet, path, nil, ts)
			hdr = http.Header{}
			hdr.Set(hmacauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
			hdr.Set(hmacauth.HeaderSignature, sig)
		}
		dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
		defer dcancel()
		c2, _, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{HTTPClient: f.HTTPClient(), HTTPHeader: hdr})
		if !assert.NoErrorf(c, err, "websocket dial %q", wsURL) {
			return
		}
		// One bounded context covers the write + read round-trip (coder/websocket
		// has no SetReadDeadline; the context carries the deadline instead).
		ioCtx, iocancel := context.WithTimeout(ctx, 10*time.Second)
		defer iocancel()
		if err := c2.Write(ioCtx, websocket.MessageText, []byte("hello-from-test")); !assert.NoError(c, err, "websocket write") {
			_ = c2.CloseNow()
			return
		}
		_, msg, err := c2.Read(ioCtx)
		if !assert.NoError(c, err, "websocket read") {
			_ = c2.CloseNow()
			return
		}
		if !assert.Equalf(c, "hello-from-test", string(msg),
			"broadcast.js should echo the sent frame back to the same client (got %q)", string(msg)) {
			_ = c2.CloseNow()
			return
		}
		conn = c2
	}, 90*time.Second, 2*time.Second)
	require.NotNil(t, conn, "websocket round-trip never succeeded")
	// Politely close with a normal-closure frame.
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
}
