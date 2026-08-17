// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// resultText returns the concatenated text content of a CallToolResult.
func resultText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range r.Content {
		tc, ok := c.(*mcp.TextContent)
		require.True(t, ok, "expected TextContent")
		b.WriteString(tc.Text)
	}
	return b.String()
}

func TestProxyInvokeSignsAndForwards(t *testing.T) {
	t.Parallel()
	master := []byte("master-secret")

	var gotPath, gotBody, gotTS, gotSig, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTS = r.Header.Get(hmacauth.HeaderTimestamp)
		gotSig = r.Header.Get(hmacauth.HeaderSignature)
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from function"))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, master, logr.Discard(), 0)
	e := ToolEntry{ToolName: "t", Namespace: "ns", FnName: "fn"}
	res, err := p.Invoke(context.Background(), e, []byte(`{"q":"hi"}`))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)
	assert.Equal(t, "hello from function", resultText(t, res))

	assert.Equal(t, "/fission-function/ns/fn", gotPath)
	assert.Equal(t, `{"q":"hi"}`, gotBody)
	assert.Equal(t, "application/json", gotCT)
	assert.NotEmpty(t, gotTS, "request must be HMAC-signed (timestamp header)")
	assert.NotEmpty(t, gotSig, "request must be HMAC-signed (signature header)")
}

// TestProxyInvokeFoldsDefaultNamespace asserts a default-namespace function maps
// to the folded /fission-function/<name> route (not /fission-function/default/<name>).
func TestProxyInvokeFoldsDefaultNamespace(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	_, err := p.Invoke(context.Background(), ToolEntry{Namespace: "default", FnName: "fn"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "/fission-function/fn", gotPath, "default namespace must be folded out of the path")
}

// TestProxyInvokeAliasSuffix asserts an alias-addressed ToolEntry (RFC-0025)
// proxies to the ":<alias>" internal route instead of the live function's
// bare route.
func TestProxyInvokeAliasSuffix(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	_, err := p.Invoke(context.Background(), ToolEntry{Namespace: "ns", FnName: "fn", Alias: "blue"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "/fission-function/ns/fn:blue", gotPath)
}

func TestProxyInvokeUnsignedWhenNoMaster(t *testing.T) {
	t.Parallel()
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(hmacauth.HeaderSignature)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	_, err := p.Invoke(context.Background(), ToolEntry{Namespace: "ns", FnName: "fn"}, nil)
	require.NoError(t, err)
	assert.Empty(t, gotSig, "no master ⇒ unsigned (pass-through)")
}

func TestProxyInvokeStatusMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		status       int
		body         string
		wantIsError  bool
		wantContains string
	}{
		{"2xx success", http.StatusOK, "ok-body", false, "ok-body"},
		{"4xx surfaces body", http.StatusBadRequest, "bad input", true, "function returned 400"},
		{"5xx generic", http.StatusInternalServerError, "stacktrace-leak", true, "function invocation failed"},
		{"404 surfaces body", http.StatusNotFound, "no route", true, "function returned 404"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			p := NewProxy(srv.URL, nil, logr.Discard(), 0)
			res, err := p.Invoke(context.Background(), ToolEntry{Namespace: "ns", FnName: "fn"}, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantIsError, res.IsError)
			assert.Contains(t, resultText(t, res), tc.wantContains)
			if tc.status >= 500 {
				assert.NotContains(t, resultText(t, res), "stacktrace-leak", "5xx body must not leak to the agent")
			}
		})
	}
}

func TestProxyInvokeOversizedResponseCapped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	p.maxBody = 10 // force the cap
	res, err := p.Invoke(context.Background(), ToolEntry{Namespace: "ns", FnName: "fn"}, nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "exceeded 10 bytes")
}

// TestProxyInvokeStreamingForwardsChunks: the handler writes two flushed
// chunks, gated so the second is only written after the first notification
// arrived — the proxy must observe at least two chunks whose concatenation
// equals the final result text, with strictly increasing progress bytes.
func TestProxyInvokeStreamingForwardsChunks(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// SAFETY: httptest.Server hands handlers a ResponseWriter that implements http.Flusher.
		f := w.(http.Flusher)
		_, _ = w.Write([]byte("hello "))
		f.Flush()
		<-release
		_, _ = w.Write([]byte("world"))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	e := ToolEntry{Namespace: "ns", FnName: "fn", Streaming: true}
	var mu sync.Mutex
	var chunks []string
	var progress []int64
	notify := func(_ context.Context, chunk string, progressBytes int64) error {
		mu.Lock()
		defer mu.Unlock()
		chunks = append(chunks, chunk)
		progress = append(progress, progressBytes)
		if len(chunks) == 1 {
			close(release)
		}
		return nil
	}
	res, err := p.InvokeStreaming(t.Context(), e, nil, notify)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(chunks), 2, "flushed chunks must arrive incrementally")
	assert.Equal(t, strings.Join(chunks, ""), resultText(t, res),
		"progress chunks must concatenate to the final result")
	for i := 1; i < len(progress); i++ {
		assert.Greater(t, progress[i], progress[i-1], "progress must be monotonic")
	}
}

// TestProxyInvokeStreamingRuneBoundary: a multi-byte rune split across two
// flushes must never surface as an invalid-UTF-8 progress chunk; the partial
// rune is held back until its remaining bytes arrive.
func TestProxyInvokeStreamingRuneBoundary(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// SAFETY: httptest.Server hands handlers a ResponseWriter that implements http.Flusher.
		f := w.(http.Flusher)
		// "ab" + first 2 of the 3 bytes of "€": the emitable prefix is "ab".
		_, _ = w.Write([]byte("ab\xe2\x82"))
		f.Flush()
		<-release
		// The final byte of "€", then "cd".
		_, _ = w.Write([]byte("\xaccd"))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	e := ToolEntry{Namespace: "ns", FnName: "fn", Streaming: true}
	var mu sync.Mutex
	var chunks []string
	notify := func(_ context.Context, chunk string, _ int64) error {
		mu.Lock()
		defer mu.Unlock()
		chunks = append(chunks, chunk)
		if len(chunks) == 1 {
			close(release)
		}
		return nil
	}
	res, err := p.InvokeStreaming(t.Context(), e, nil, notify)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Equal(t, "ab€cd", resultText(t, res))

	mu.Lock()
	defer mu.Unlock()
	for _, c := range chunks {
		assert.True(t, utf8.ValidString(c), "chunk %q must be valid UTF-8", c)
	}
	assert.Equal(t, "ab€cd", strings.Join(chunks, ""))
}

// TestProxyInvokeStreamingCapAborts: a function streaming past maxBody gets
// the same IsError cap result as the buffered path, and the proxy cancels the
// upstream read instead of draining an unbounded stream (the handler here
// streams until the client goes away — the call returning at all proves the
// cancel).
func TestProxyInvokeStreamingCapAborts(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SAFETY: httptest.Server hands handlers a ResponseWriter that implements http.Flusher.
		f := w.(http.Flusher)
		for {
			if _, err := w.Write(make([]byte, 1024)); err != nil {
				return
			}
			f.Flush()
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	p.maxBody = 16
	e := ToolEntry{Namespace: "ns", FnName: "fn", Streaming: true}
	res, err := p.InvokeStreaming(t.Context(), e, nil, func(context.Context, string, int64) error { return nil })
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "exceeded 16 bytes")
}

// TestProxyInvokeStreamingIdleTimeout: no bytes within StreamIdleTimeout
// aborts the stream with the timeout tool error.
func TestProxyInvokeStreamingIdleTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SAFETY: httptest.Server hands handlers a ResponseWriter that implements http.Flusher.
		f := w.(http.Flusher)
		_, _ = w.Write([]byte("x"))
		f.Flush()
		<-r.Context().Done() // never write again; unblocks when the proxy cancels
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	e := ToolEntry{Namespace: "ns", FnName: "fn", Streaming: true, StreamIdleTimeout: 50 * time.Millisecond}
	start := time.Now()
	res, err := p.InvokeStreaming(t.Context(), e, nil, func(context.Context, string, int64) error { return nil })
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "timed out")
	assert.Less(t, time.Since(start), 5*time.Second, "idle timeout must fire promptly, not wait for a hard ceiling")
}

// TestProxyInvokeStreamingNon2xxNoNotify: error statuses take the buffered
// mapping — nothing is streamed as progress.
func TestProxyInvokeStreamingNon2xxNoNotify(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such route"))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	e := ToolEntry{Namespace: "ns", FnName: "fn", Streaming: true}
	notified := false
	res, err := p.InvokeStreaming(t.Context(), e, nil, func(context.Context, string, int64) error { notified = true; return nil })
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "function returned 404")
	assert.False(t, notified, "non-2xx must not emit progress")
}

// TestProxyInvokeStreamingEOFPartialRuneProgress: a body ending mid-rune must
// still deliver strictly increasing progress — the EOF flush reports
// cumulative DELIVERED bytes rather than repeating the read-side total that
// already covered the held-back partial rune.
func TestProxyInvokeStreamingEOFPartialRuneProgress(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// SAFETY: httptest.Server hands handlers a ResponseWriter that implements http.Flusher.
		f := w.(http.Flusher)
		// "X" + the first 2 of the 3 bytes of "€", then EOF mid-rune.
		_, _ = w.Write([]byte("X\xe2\x82"))
		f.Flush()
		<-release
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	e := ToolEntry{Namespace: "ns", FnName: "fn", Streaming: true}
	var mu sync.Mutex
	var chunks []string
	var progress []int64
	notify := func(_ context.Context, chunk string, progressBytes int64) error {
		mu.Lock()
		defer mu.Unlock()
		chunks = append(chunks, chunk)
		progress = append(progress, progressBytes)
		if len(chunks) == 1 {
			close(release)
		}
		return nil
	}
	res, err := p.InvokeStreaming(t.Context(), e, nil, notify)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Equal(t, "X\xe2\x82", resultText(t, res))

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(chunks), 2, "the held-back partial rune must flush at EOF")
	assert.Equal(t, "X\xe2\x82", strings.Join(chunks, ""))
	for i := 1; i < len(progress); i++ {
		assert.Greater(t, progress[i], progress[i-1], "progress must be strictly increasing across the EOF flush")
	}
	assert.Equal(t, int64(3), progress[len(progress)-1], "final progress equals total delivered bytes")
}

// TestProxyInvokeStreamingCallerGoneUnwinds pins the proxy's client-gone
// signal for legacy-protocol callers (whose HTTP cancellation the SDK does
// not propagate into the handler ctx): once notify reports the caller
// unreachable, the upstream call must be cancelled promptly — NOT run until
// the function EOFs — and classified as canceled. The handler streams
// forever until its request ctx is cancelled, so the call returning at all
// proves the unwind.
func TestProxyInvokeStreamingCallerGoneUnwinds(t *testing.T) {
	t.Parallel()
	upstreamCancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SAFETY: httptest.Server hands handlers a ResponseWriter that implements http.Flusher.
		f := w.(http.Flusher)
		defer close(upstreamCancelled)
		for {
			if _, err := w.Write([]byte("tick\n")); err != nil {
				return
			}
			f.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard(), 0)
	e := ToolEntry{Namespace: "ns", FnName: "fn", Streaming: true, StreamIdleTimeout: time.Hour}
	gone := errors.New("stream not connected or already closed")
	start := time.Now()
	res, err := p.InvokeStreaming(t.Context(), e, nil, func(context.Context, string, int64) error { return gone })
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "canceled")
	assert.Less(t, time.Since(start), 5*time.Second, "an unreachable caller must unwind the call promptly, not wait for EOF/idle")

	select {
	case <-upstreamCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request must be cancelled when the caller is gone")
	}
}
