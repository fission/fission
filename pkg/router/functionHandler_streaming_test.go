// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// fixedURLExecutor returns a fixed host:port for GetServiceForFunction and
// records tap/untap calls. Used to point the handler at a test upstream.
type fixedURLExecutor struct{ hostPort string }

func (e *fixedURLExecutor) GetServiceForFunction(_ context.Context, _ *fv1.Function) (string, error) {
	return e.hostPort, nil
}
func (e *fixedURLExecutor) TapService(metav1.ObjectMeta, fv1.ExecutorType, url.URL) {}
func (e *fixedURLExecutor) UnTapService(context.Context, metav1.ObjectMeta, fv1.ExecutorType, *url.URL) error {
	return nil
}

// chunkedUpstream serves `n` lines, flushing and sleeping `gap` between each.
func chunkedUpstream(n int, gap time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := range n {
			fmt.Fprintf(w, "line-%d\n", i)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(gap)
		}
	}))
}

func streamingFn(uid string, sc *fv1.StreamingConfig) *fv1.Function {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: k8stypes.UID(uid)},
	}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypePoolmgr
	fn.Spec.Streaming = sc
	return fn
}

// newHandlerForUpstream builds a minimal poolmgr functionHandler pointed at the
// given upstream, with the given per-function FunctionTimeout (seconds).
func newHandlerForUpstream(t *testing.T, fn *fv1.Function, upstream *httptest.Server, fnTimeoutSec int) functionHandler {
	t.Helper()
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	return functionHandler{
		logger:   loggerfactory.GetLogger(),
		function: fn,
		executor: &fixedURLExecutor{hostPort: u.Host},
		functionTimeoutMap: map[k8stypes.UID]int{
			fn.GetUID(): fnTimeoutSec,
		},
		tsRoundTripperParams: &tsRoundTripperParams{
			timeout:           5 * time.Second,
			timeoutExponent:   2,
			keepAliveTime:     30 * time.Second,
			maxRetries:        2,
			svcAddrRetryCount: 2,
			streamIdleDefault: 60 * time.Second,
		},
	}
}

// countLines reads the proxied response body and returns how many lines arrived
// before EOF/close.
func countLines(t *testing.T, serverURL string) int {
	t.Helper()
	resp, err := http.Get(serverURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	n := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		n++
	}
	return n
}

// TestStreamingSurvivesPastFunctionTimeout: a streaming function (no explicit
// MaxDuration) must deliver the WHOLE stream even though it runs well past the
// function's FunctionTimeout — proving streaming does not inherit the
// total-wall-clock cap. The matched classic function IS cut at FunctionTimeout.
func TestStreamingSurvivesPastFunctionTimeout(t *testing.T) {
	t.Parallel()
	const chunks = 4
	const gap = 400 * time.Millisecond // total ~1.6s of streaming
	const fnTimeoutSec = 1             // shorter than the total stream

	t.Run("streaming delivers all chunks", func(t *testing.T) {
		t.Parallel()
		upstream := chunkedUpstream(chunks, gap)
		defer upstream.Close()
		fn := streamingFn("stream-uid", &fv1.StreamingConfig{Enabled: true})
		fh := newHandlerForUpstream(t, fn, upstream, fnTimeoutSec)
		router := httptest.NewServer(http.HandlerFunc(fh.handler))
		defer router.Close()

		got := countLines(t, router.URL)
		assert.Equal(t, chunks, got, "streaming response must not be cut at FunctionTimeout")
	})

	t.Run("classic is cut at FunctionTimeout", func(t *testing.T) {
		t.Parallel()
		upstream := chunkedUpstream(chunks, gap)
		defer upstream.Close()
		fn := streamingFn("classic-uid", nil) // no streaming
		fh := newHandlerForUpstream(t, fn, upstream, fnTimeoutSec)
		router := httptest.NewServer(http.HandlerFunc(fh.handler))
		defer router.Close()

		got := countLines(t, router.URL)
		assert.Less(t, got, chunks, "classic response should be cut by FunctionTimeout before all chunks arrive")
	})
}

// TestWebSocketSurvivesIdlePastFunctionTimeout proves a hijacked WebSocket stays
// open while idle past the function's FunctionTimeout — the router tap holds the
// pod and the stream context carries no wall-clock ceiling. This is the
// environment-agnostic replacement for the legacy /wsevent keepalive.
func TestWebSocketSurvivesIdlePastFunctionTimeout(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	fn := streamingFn("ws-uid", &fv1.StreamingConfig{Enabled: true, Protocol: fv1.StreamingWebSocket})
	fh := newHandlerForUpstream(t, fn, upstream, 1 /* FunctionTimeout: 1s */)
	router := httptest.NewServer(http.HandlerFunc(fh.handler))
	defer router.Close()

	wsURL := "ws://" + strings.TrimPrefix(router.URL, "http://") + "/"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	// First echo.
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("ping-1")))
	_, got, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, "ping-1", string(got))

	// Stay idle well past the 1s FunctionTimeout, then echo again. A classic
	// (non-streaming) proxy would have torn the connection down by now.
	time.Sleep(1500 * time.Millisecond)

	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("ping-2")))
	_, got, err = conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, "ping-2", string(got), "socket must survive idle past FunctionTimeout")
}
