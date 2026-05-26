// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logdb

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMakeIndexMap(t *testing.T) {
	t.Parallel()
	got := makeIndexMap([]string{"time", "log", "stream"})
	assert.Equal(t, map[string]int{"time": 0, "log": 1, "stream": 2}, got)
	assert.Empty(t, makeIndexMap(nil))
}

func TestGetEntryValue(t *testing.T) {
	t.Parallel()
	row := []any{"a", nil, "c"}

	assert.Equal(t, "a", getEntryValue(row, 0))
	assert.Equal(t, "c", getEntryValue(row, 2, -1))
	// primary index nil -> falls back to a valid index
	assert.Equal(t, "c", getEntryValue(row, 1, 2))
	// primary index out of range -> falls back
	assert.Equal(t, "a", getEntryValue(row, 99, 0))
	// nothing valid -> empty string
	assert.Equal(t, "", getEntryValue(row, 1, -1))
	assert.Equal(t, "", getEntryValue(row, 99, 100))
}

func TestNewInfluxDB(t *testing.T) {
	t.Setenv("influxdb_URL", "http://influxdb.example/query")
	db, err := NewInfluxDB(t.Context(), LogDBOptions{})
	require.NoError(t, err)
	assert.Equal(t, "http://influxdb.example/query", db.endpoint)
}

// influxResponse is a minimal canned InfluxDB query response with two log rows.
const influxResponse = `{"results":[{"series":[{"name":"log","columns":` +
	`["time","_seq","log","kubernetes_namespace_name","kubernetes_pod_name","stream","kubernetes_labels_functionName","kubernetes_labels_functionUid","kubernetes_docker_id"],` +
	`"values":[` +
	`["2021-06-01T10:00:01Z","2","second line\n","ns","pod1","stdout","hello","uid-1","docker1"],` +
	`["2021-06-01T10:00:00Z","1","first line\n","ns","pod1","stdout","hello","uid-1","docker1"]` +
	`]}]}]}`

func TestInfluxDBGetLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(influxResponse))
	}))
	defer srv.Close()
	influx := InfluxDB{endpoint: srv.URL}

	t.Run("plain output sorted ascending", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, influx.GetLogs(t.Context(), LogFilter{FuncUid: "uid-1", RecordLimit: 10}, &buf))
		out := buf.String()
		assert.Contains(t, out, "first line")
		assert.Contains(t, out, "second line")
		assert.Less(t, bytes.Index(buf.Bytes(), []byte("first line")), bytes.Index(buf.Bytes(), []byte("second line")),
			"ascending order should place the earlier timestamp first")
	})

	t.Run("details output includes metadata", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, influx.GetLogs(t.Context(), LogFilter{FuncUid: "uid-1", RecordLimit: 10, Details: true}, &buf))
		out := buf.String()
		assert.Contains(t, out, "Function Name: hello")
		assert.Contains(t, out, "Pod: pod1")
		assert.Contains(t, out, "Container: docker1")
	})

	t.Run("pod filter and reverse order", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, influx.GetLogs(t.Context(), LogFilter{FuncUid: "uid-1", Pod: "pod1", RecordLimit: 10, Reverse: true}, &buf))
		out := buf.String()
		assert.Greater(t, bytes.Index(buf.Bytes(), []byte("first line")), bytes.Index(buf.Bytes(), []byte("second line")),
			"reverse order should place the later timestamp first")
		assert.Contains(t, out, "first line")
	})
}

func TestInfluxDBGetLogsQueryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	influx := InfluxDB{endpoint: srv.URL}

	var buf bytes.Buffer
	err := influx.GetLogs(t.Context(), LogFilter{FuncUid: "uid-1", RecordLimit: 10}, &buf)
	require.Error(t, err)
}
