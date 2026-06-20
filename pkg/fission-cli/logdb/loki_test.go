// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logdb

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildLogQL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filter   LogFilter
		wantErr  bool
		contains []string
		absent   []string
	}{
		{
			name:     "uid + namespace selectors",
			filter:   LogFilter{FuncUid: "u1", PodNamespace: "default"},
			contains: []string{`fission_function_uid="u1"`, `fission_function_namespace="default"`},
			absent:   []string{"| json"},
		},
		{
			name:     "correlation filters add a json pipeline",
			filter:   LogFilter{FuncUid: "u1", RequestID: "req-9", TraceID: "abc", Level: "error"},
			contains: []string{`fission_function_uid="u1"`, "| json", `fission_request_id="req-9"`, `trace_id="abc"`, `level="error"`},
		},
		{
			name:     "falls back to function name when no label selector",
			filter:   LogFilter{Function: "myfn"},
			contains: []string{`fission_function_name="myfn"`},
		},
		{
			name:    "errors when nothing is selectable (avoids an empty Loki matcher)",
			filter:  LogFilter{},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := buildLogQL(tc.filter)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			for _, c := range tc.contains {
				assert.Contains(t, q, c)
			}
			for _, a := range tc.absent {
				assert.NotContains(t, q, a)
			}
		})
	}
}

func TestLokiGetLogs(t *testing.T) {
	var gotQuery, gotDirection, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotDirection = r.URL.Query().Get("direction")
		gotLimit = r.URL.Query().Get("limit")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"fission_function_uid":"u1","k8s_pod_name":"pod-1","fission_function_namespace":"default"},
			 "values":[["1700000000000000000","line one"],["1700000001000000000","line two"]]}]}}`))
	}))
	defer srv.Close()

	t.Setenv("LOKI_URL", srv.URL)
	l, err := NewLoki(t.Context(), LogDBOptions{})
	require.NoError(t, err)

	buf := new(bytes.Buffer)
	err = l.GetLogs(t.Context(), LogFilter{
		FuncUid: "u1", PodNamespace: "default", RequestID: "req-9", RecordLimit: 100, Reverse: true,
	}, buf)
	require.NoError(t, err)

	// LogQL + query params built from the filter.
	assert.Contains(t, gotQuery, `fission_function_uid="u1"`)
	assert.Contains(t, gotQuery, `fission_request_id="req-9"`)
	assert.Equal(t, "backward", gotDirection, "Reverse=true must map to backward")
	assert.Equal(t, "100", gotLimit)

	// Lines parsed out of the streams response and written in order.
	out := buf.String()
	assert.Contains(t, out, "line one")
	assert.Contains(t, out, "line two")
}

// TestLokiGetLogsFloorsEpochStart guards the regression where the CLI's default
// Since (the epoch) made the query_range span decades, which Loki rejects with
// "the query time range exceeds the limit". The adapter must floor the start to
// the recent lookback window.
func TestLokiGetLogsFloorsEpochStart(t *testing.T) {
	var gotStart string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotStart = r.URL.Query().Get("start")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
	}))
	defer srv.Close()

	t.Setenv("LOKI_URL", srv.URL)
	l, err := NewLoki(t.Context(), LogDBOptions{})
	require.NoError(t, err)

	err = l.GetLogs(t.Context(), LogFilter{FuncUid: "u1", Since: time.Unix(0, 0), RecordLimit: 10}, new(bytes.Buffer))
	require.NoError(t, err)

	startNanos, perr := strconv.ParseInt(gotStart, 10, 64)
	require.NoErrorf(t, perr, "start must be a unix-nano integer, got %q", gotStart)
	start := time.Unix(0, startNanos)
	assert.WithinDuration(t, time.Now().Add(-defaultLokiLookback), start, time.Hour,
		"epoch Since must be floored to the recent lookback window, not sent as 1970")
	assert.Truef(t, start.After(time.Unix(0, 0).Add(time.Hour)),
		"start must not be the epoch; got %s", start)
}

// TestLokiStreamLogs exercises the /tail WebSocket path: a test server upgrades
// the connection, emits one tail frame, and closes; StreamLogs must render the
// line in the shared CLI format and return cleanly on the normal close.
func TestLokiStreamLogs(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		// One tail frame, then a normal close.
		_ = c.Write(r.Context(), websocket.MessageText,
			[]byte(`{"streams":[{"stream":{"k8s_pod_name":"pod-1","fission_function_uid":"u1"},"values":[["1700000000000000000","tail line one"]]}]}`))
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	l := loki{endpoint: srv.URL, client: &http.Client{Timeout: lokiHTTPTimeout}}
	var buf bytes.Buffer
	err := l.StreamLogs(t.Context(), LogFilter{FuncUid: "u1", RecordLimit: 10}, &buf)
	require.NoError(t, err)

	assert.Equal(t, lokiTailPath, gotPath, "tail uses the /tail endpoint")
	assert.Contains(t, gotQuery, `fission_function_uid="u1"`, "tail query is built from the filter")
	assert.Contains(t, buf.String(), "tail line one", "the tailed line is rendered")
}

func TestLokiEntry(t *testing.T) {
	t.Parallel()
	entry, err := lokiEntry(
		map[string]string{"fission_function_uid": "u1", "k8s_pod_name": "p1", "fission_function_name": "fn"},
		[2]string{"1700000000000000000", "hello\n"},
	)
	require.NoError(t, err)
	assert.Equal(t, "hello", entry.Message, "trailing newline trimmed")
	assert.Equal(t, "u1", entry.FuncUid)
	assert.Equal(t, "p1", entry.Pod)
	assert.Equal(t, "fn", entry.FuncName)

	_, err = lokiEntry(map[string]string{}, [2]string{"not-a-number", "x"})
	require.Error(t, err, "a non-numeric timestamp is an error")
}
