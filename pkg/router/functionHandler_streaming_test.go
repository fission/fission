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
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/throttler"
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

func (e *fixedURLExecutor) EnsureCapacity(context.Context, *fv1.Function, int, int) (string, error) {
	return "", ferror.MakeError(ferror.ErrorNotFound, "fake executor has no capacity endpoint")
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

// countingExecutor records Tap/UnTap calls so tests can assert the keepalive
// heartbeat and the untap-once-on-drain contract.
type countingExecutor struct {
	hostPort string
	taps     atomic.Int64
	untaps   atomic.Int64
}

func (e *countingExecutor) GetServiceForFunction(_ context.Context, _ *fv1.Function) (string, error) {
	return e.hostPort, nil
}
func (e *countingExecutor) TapService(metav1.ObjectMeta, fv1.ExecutorType, url.URL) { e.taps.Add(1) }
func (e *countingExecutor) UnTapService(context.Context, metav1.ObjectMeta, fv1.ExecutorType, *url.URL) error {
	e.untaps.Add(1)
	return nil
}

func (e *countingExecutor) EnsureCapacity(context.Context, *fv1.Function, int, int) (string, error) {
	return "", ferror.MakeError(ferror.ErrorNotFound, "fake executor has no capacity endpoint")
}

// newHandlerForUpstream builds a minimal poolmgr functionHandler pointed at the
// given upstream, with the given per-function FunctionTimeout (seconds).
func newHandlerForUpstream(t *testing.T, fn *fv1.Function, upstream *httptest.Server, fnTimeoutSec int) functionHandler {
	t.Helper()
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	return newStreamingHandler(t, fn, &fixedURLExecutor{hostPort: u.Host}, fnTimeoutSec, 60*time.Second)
}

// newStreamingHandler builds a poolmgr functionHandler with an explicit executor
// and a (possibly sub-second) default stream idle timeout, for abort/keepalive tests.
func newStreamingHandler(t *testing.T, fn *fv1.Function, exec eclient.ClientInterface, fnTimeoutSec int, idleDefault time.Duration) functionHandler {
	t.Helper()
	logger := loggerfactory.GetLogger()
	return functionHandler{
		logger:   logger,
		function: fn,
		resolver: &executorResolver{
			logger:    logger,
			fmap:      makeFunctionServiceMap(logger, time.Minute),
			executor:  exec,
			throttler: throttler.MakeThrottler(30 * time.Second),
		},
		tapper: &executorTapper{logger: logger, executor: exec, unTapTimeout: time.Hour},
		functionTimeoutMap: map[crd.CacheKeyUG]int{
			crd.CacheKeyUGFromMeta(&fn.ObjectMeta): fnTimeoutSec,
		},
		tsRoundTripperParams: &tsRoundTripperParams{
			timeout:           5 * time.Second,
			timeoutExponent:   2,
			keepAliveTime:     30 * time.Second,
			maxRetries:        2,
			svcAddrRetryCount: 2,
			streamIdleDefault: idleDefault,
		},
	}
}

// TestStreamingIdleTimeoutAbortsStalledStream: a stream that goes idle longer
// than the idle window is aborted (truncated) rather than hanging forever.
func TestStreamingIdleTimeoutAbortsStalledStream(t *testing.T) {
	t.Parallel()
	// upstream sends one chunk, flushes, then stalls far longer than the idle window.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fmt.Fprint(w, "chunk-1\n")
			fl.Flush()
		}
		// would-be chunk-2, long after the idle window; exit promptly when the
		// proxy aborts the connection so the test doesn't block on cleanup.
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	fn := streamingFn("idle-uid", &fv1.StreamingConfig{}) // default idle from harness
	fh := newStreamingHandler(t, fn, &fixedURLExecutor{hostPort: u.Host}, 0 /* no fnTimeout */, 200*time.Millisecond)
	router := httptest.NewServer(http.HandlerFunc(fh.handler))
	defer router.Close()

	startT := time.Now()
	got := countLines(t, router.URL)
	elapsed := time.Since(startT)

	assert.Equal(t, 1, got, "only the first chunk should arrive before the idle abort")
	assert.Less(t, elapsed, 3*time.Second, "stream must be aborted on idle, not hang for the full upstream sleep")
}

// TestStreamingMaxDurationCutsActiveStream: an actively-streaming response (chunks
// flowing, idle never fires) is still cut at the max-duration ceiling.
func TestStreamingMaxDurationCutsActiveStream(t *testing.T) {
	t.Parallel()
	// upstream streams a chunk every 100ms for ~3s — idle never triggers.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := range 30 {
			if r.Context().Err() != nil { // proxy cut the connection
				return
			}
			fmt.Fprintf(w, "chunk-%d\n", i)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	// 1s ceiling, large idle so only max-duration can cut it.
	fn := streamingFn("max-uid", &fv1.StreamingConfig{IdleTimeoutSeconds: 30, MaxDurationSeconds: 1})
	fh := newStreamingHandler(t, fn, &fixedURLExecutor{hostPort: u.Host}, 0, 30*time.Second)
	router := httptest.NewServer(http.HandlerFunc(fh.handler))
	defer router.Close()

	startT := time.Now()
	got := countLines(t, router.URL)
	elapsed := time.Since(startT)

	assert.Greater(t, got, 0, "some chunks should arrive before the ceiling")
	assert.Less(t, got, 30, "the active stream must be cut at the max-duration ceiling")
	assert.Less(t, elapsed, 2500*time.Millisecond, "must be cut near the 1s ceiling, not run the full ~3s")
}

// TestStreamingKeepaliveRetapsPod: a long stream re-taps the pod on the heartbeat
// interval, and untaps exactly once after the stream drains.
func TestStreamingKeepaliveRetapsPod(t *testing.T) {
	t.Parallel()
	// upstream streams for ~500ms then completes.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := range 5 {
			fmt.Fprintf(w, "chunk-%d\n", i)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	exec := &countingExecutor{hostPort: u.Host}
	// idle 200ms => heartbeat interval 100ms => ~5 taps across a ~500ms stream.
	fn := streamingFn("keepalive-uid", &fv1.StreamingConfig{})
	fh := newStreamingHandler(t, fn, exec, 0, 200*time.Millisecond)
	router := httptest.NewServer(http.HandlerFunc(fh.handler))
	defer router.Close()

	got := countLines(t, router.URL)
	require.Equal(t, 5, got)

	assert.GreaterOrEqual(t, exec.taps.Load(), int64(2), "keepalive heartbeat should re-tap the pod during the stream")

	// untap fires from a deferred goroutine after ServeHTTP returns; give it a moment.
	assert.Eventually(t, func() bool { return exec.untaps.Load() == 1 }, 2*time.Second, 20*time.Millisecond,
		"the pod must be untapped exactly once after the stream drains")
}

// TestWebSocketRespectsMaxDuration: a hijacked WebSocket with a max-duration
// ceiling is torn down at the ceiling even while idle (the only timeout that
// applies to a socket without observable body reads).
func TestWebSocketRespectsMaxDuration(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	fn := streamingFn("ws-max-uid", &fv1.StreamingConfig{Protocol: fv1.StreamingWebSocket, MaxDurationSeconds: 1})
	fh := newStreamingHandler(t, fn, &fixedURLExecutor{hostPort: u.Host}, 0, 30*time.Second)
	router := httptest.NewServer(http.HandlerFunc(fh.handler))
	defer router.Close()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	wsURL := "ws://" + strings.TrimPrefix(router.URL, "http://") + "/"
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	// Idle socket: no messages. The 1s ceiling must close it. Bound the read so a
	// failure to enforce the ceiling fails the test instead of hanging.
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	_, _, err = conn.Read(readCtx)
	require.Error(t, err, "socket must be closed at the max-duration ceiling")
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
		fn := streamingFn("stream-uid", &fv1.StreamingConfig{})
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

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			mt, msg, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), mt, msg); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	fn := streamingFn("ws-uid", &fv1.StreamingConfig{Protocol: fv1.StreamingWebSocket})
	fh := newHandlerForUpstream(t, fn, upstream, 1 /* FunctionTimeout: 1s */)
	router := httptest.NewServer(http.HandlerFunc(fh.handler))
	defer router.Close()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	wsURL := "ws://" + strings.TrimPrefix(router.URL, "http://") + "/"
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	// One context for all read/writes; it outlives the idle sleep below but
	// bounds any single op so a regression fails instead of hanging.
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer wsCancel()

	// First echo.
	require.NoError(t, conn.Write(wsCtx, websocket.MessageText, []byte("ping-1")))
	_, got, err := conn.Read(wsCtx)
	require.NoError(t, err)
	require.Equal(t, "ping-1", string(got))

	// Stay idle well past the 1s FunctionTimeout, then echo again. A classic
	// (non-streaming) proxy would have torn the connection down by now.
	time.Sleep(1500 * time.Millisecond)

	require.NoError(t, conn.Write(wsCtx, websocket.MessageText, []byte("ping-2")))
	_, got, err = conn.Read(wsCtx)
	require.NoError(t, err)
	require.Equal(t, "ping-2", string(got), "socket must survive idle past FunctionTimeout")
}

// TestStreamingRouterAdmittedSlotReleasedAfterDrain pins the handler-level
// settle of a ROUTER-ADMITTED streaming slot: the per-resolve transport defers
// skip streaming requests, so the handler defer is the only thing returning
// the serving slot — re-gating it on poolmgr-untap-only (the pre-phase-4
// shape) would pin the pod's in-flight counter forever. Also asserts the
// keepalive heartbeat taps the TAP target (Service address), not the dial
// target (pod IP), for endpoint-LB-shaped entries.
func TestStreamingRouterAdmittedSlotReleasedAfterDrain(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := range 3 {
			fmt.Fprintf(w, "chunk-%d\n", i)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer upstream.Close()
	dial, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	tapTarget := mustParseURL(t, "http://svc-stream.default:80")

	resolver := &releaseTrackingResolver{answers: []*url.URL{dial}, tapURL: tapTarget}
	tapper := &nopTapper{}
	fn := streamingFn("admitted-stream-uid", &fv1.StreamingConfig{})
	logger := loggerfactory.GetLogger()
	fh := functionHandler{
		logger:   logger,
		function: fn,
		resolver: resolver,
		tapper:   tapper,
		functionTimeoutMap: map[crd.CacheKeyUG]int{
			crd.CacheKeyUGFromMeta(&fn.ObjectMeta): 0,
		},
		tsRoundTripperParams: &tsRoundTripperParams{
			timeout:           5 * time.Second,
			timeoutExponent:   2,
			keepAliveTime:     30 * time.Second,
			maxRetries:        2,
			svcAddrRetryCount: 2,
			streamIdleDefault: 200 * time.Millisecond,
		},
	}
	router := httptest.NewServer(http.HandlerFunc(fh.handler))
	defer router.Close()

	got := countLines(t, router.URL)
	require.Equal(t, 3, got, "the stream must drain fully")

	// The slot is released exactly once after the drain, and the release path
	// (router-local accounting) must NOT fire the UnTap RPC.
	require.Eventually(t, func() bool {
		resolver.mu.Lock()
		defer resolver.mu.Unlock()
		return len(resolver.released) > 0 && resolver.released[len(resolver.released)-1].Load() == 1
	}, 2*time.Second, 20*time.Millisecond, "the handler settle must release the streaming slot after drain")
	assert.Zero(t, tapper.untaps.Load(), "router-admitted entries never UnTap")

	// Heartbeat taps (if any fired during the ~300ms stream) target the tap
	// URL, not the dialed pod address.
	if tapper.taps.Load() > 0 {
		assert.Equal(t, tapTarget.Host, tapper.lastTap.Load().Host,
			"keepalive taps must key on the Service address")
	}
}
