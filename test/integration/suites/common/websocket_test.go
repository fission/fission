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

// TestWebsocket is the Go port of test/tests/websocket/test_ws.sh.
//
// Creates a Node.js function exporting a websocket-aware handler
// (broadcast.js: echoes received messages back to all connected clients,
// including the sender), then dials the router's well-known internal
// route ws://<router>/fission-function/<fn>, sends a single text frame,
// and asserts the same frame comes back.
//
// Skipped when the router is reachable only via http (no ws scheme); the
// transport here just rewrites http→ws on f.Router().BaseURL().
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
	// http://127.0.0.1:8889 → ws://127.0.0.1:8889.
	base := f.RouterInternalBaseURL()
	require.True(t, strings.HasPrefix(base, "http://"),
		"router internal base URL must be http:// for ws rewrite, got %q", base)
	path := "/fission-function/" + fnName
	wsURL := "ws://" + strings.TrimPrefix(base, "http://") + path

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
	// until dial succeeds. Re-sign on every attempt so the timestamp
	// stays inside the verifier's skew window.
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
		var err error
		conn, _, err = websocket.DefaultDialer.DialContext(dctx, wsURL, hdr)
		assert.NoErrorf(c, err, "websocket dial %q", wsURL)
	}, 90*time.Second, 2*time.Second)
	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(15*time.Second)))
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("hello-from-test")))

	_, msg, err := conn.ReadMessage()
	require.NoError(t, err, "websocket read")
	require.Equal(t, "hello-from-test", string(msg),
		"broadcast.js should echo the sent frame back to the same client")

	// Polite close.
	_ = conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}
