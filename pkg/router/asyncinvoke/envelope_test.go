// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvelopeRoundTripWithDestinations(t *testing.T) {
	t.Parallel()
	env := Envelope{
		Version: EnvelopeVersion, Namespace: "ns", Function: "fn", Method: "POST",
		Body: []byte("x"), EnqueueTime: time.Unix(1000, 0).UTC(), Depth: 1,
		OnSuccess: &Destination{FunctionNamespace: "ns", FunctionName: "next"},
		OnFailure: &Destination{Topic: "dlq", MQType: "kafka"},
	}
	data, err := env.Encode()
	require.NoError(t, err)
	got, err := Decode(data)
	require.NoError(t, err)
	require.Equal(t, env, got)
	assert.True(t, got.OnSuccess.IsFunction())
	assert.True(t, got.OnFailure.IsTopic())
}

func TestResultEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()
	re := ResultEnvelope{
		Version:         EnvelopeVersion,
		RequestContext:  RequestContext{InvocationID: "id", FunctionRef: "ns/fn", Condition: ConditionSuccess, Attempts: 2},
		RequestPayload:  []byte("req"),
		ResponseContext: ResponseContext{StatusCode: 200},
		ResponsePayload: []byte("resp"),
	}
	data, err := re.Encode()
	require.NoError(t, err)
	var got ResultEnvelope
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, re, got)
}

// FuzzResultEnvelopeDecode asserts the result envelope never panics on arbitrary
// bytes and re-encodes stably.
func FuzzResultEnvelopeDecode(f *testing.F) {
	seed, _ := ResultEnvelope{Version: "1.0", RequestContext: RequestContext{InvocationID: "id"}}.Encode()
	f.Add(seed)
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var re ResultEnvelope
		if json.Unmarshal(data, &re) != nil {
			return
		}
		out, err := re.Encode()
		require.NoError(t, err)
		var re2 ResultEnvelope
		require.NoError(t, json.Unmarshal(out, &re2))
	})
}

func TestEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()
	env := Envelope{
		Version:         EnvelopeVersion,
		Namespace:       "ns",
		Function:        "fn",
		Method:          "POST",
		Path:            "/x",
		Query:           "a=1",
		Headers:         map[string]string{"Content-Type": "application/json"},
		Body:            []byte("hello"),
		EnqueueTime:     time.Unix(1000, 0).UTC(),
		Depth:           2,
		FunctionTimeout: 30,
	}
	data, err := env.Encode()
	require.NoError(t, err)
	got, err := Decode(data)
	require.NoError(t, err)
	require.Equal(t, env, got)
}

// TestEnvelopeRoundTrip_FunctionVersion pins the RFC-0025 Task 5 wire
// contract: FunctionVersion marshals/unmarshals under its own "functionVersion"
// key, and the PRE-EXISTING "version" key (the wire-format schema version,
// EnvelopeVersion) is completely untouched -- a different field, never
// conflated with FunctionVersion.
func TestEnvelopeRoundTrip_FunctionVersion(t *testing.T) {
	t.Parallel()
	env := Envelope{
		Version:         EnvelopeVersion,
		Namespace:       "ns",
		Function:        "hello",
		FunctionVersion: "hello-v1",
		Method:          "POST",
		Body:            []byte("x"),
		EnqueueTime:     time.Unix(1000, 0).UTC(),
	}
	data, err := env.Encode()
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, EnvelopeVersion, raw["version"], "wire-format schema version key is untouched")
	assert.Equal(t, "hello-v1", raw["functionVersion"], "FunctionVersion marshals under its own key")

	got, err := Decode(data)
	require.NoError(t, err)
	require.Equal(t, env, got)
}

// TestEnvelopeDecode_PreTask5_NoFunctionVersionField proves an envelope
// persisted before this field existed (no "functionVersion" key at all, e.g.
// a message already sitting in the queue at deploy time) decodes cleanly with
// FunctionVersion at its zero value -- not an error, not a spurious pin.
func TestEnvelopeDecode_PreTask5_NoFunctionVersionField(t *testing.T) {
	t.Parallel()
	old := []byte(`{"version":"1.0","namespace":"ns","function":"fn","method":"POST","enqueueTime":"2020-01-01T00:00:00Z","depth":0}`)
	got, err := Decode(old)
	require.NoError(t, err)
	assert.Equal(t, EnvelopeVersion, got.Version, "wire-format version decodes unaffected")
	assert.Empty(t, got.FunctionVersion, "absent field decodes to the zero value, not an error")
}

// TestDestinationRoundTrip_AliasVersion pins the flat Destination struct's
// Alias/Version fields (RFC-0025 Task 5) round-tripping through the envelope.
func TestDestinationRoundTrip_AliasVersion(t *testing.T) {
	t.Parallel()
	env := Envelope{
		Version: EnvelopeVersion, Namespace: "ns", Function: "src", EnqueueTime: time.Unix(1000, 0).UTC(),
		OnSuccess: &Destination{FunctionNamespace: "ns", FunctionName: "next", Version: "next-v2"},
		OnFailure: &Destination{FunctionNamespace: "ns", FunctionName: "handler", Alias: "prod"},
	}
	data, err := env.Encode()
	require.NoError(t, err)
	got, err := Decode(data)
	require.NoError(t, err)
	require.Equal(t, env, got)
	assert.Equal(t, "next-v2", got.OnSuccess.Version)
	assert.Equal(t, "prod", got.OnFailure.Alias)
}

// TestDestination_FunctionRouteName pins the `:<version>`/`:<alias>` URL
// suffix grammar a fired destination's envelope.Function is built with:
// Version takes precedence over Alias (matching FunctionReference's own
// mutual-exclusivity priority), and a plain destination (neither set) is
// unsuffixed -- byte-identical to pre-Task-5 behavior.
func TestDestination_FunctionRouteName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		dest Destination
		want string
	}{
		{"plain", Destination{FunctionName: "fn"}, "fn"},
		{"version pinned", Destination{FunctionName: "fn", Version: "fn-v1"}, "fn:fn-v1"},
		{"alias pinned", Destination{FunctionName: "fn", Alias: "prod"}, "fn:prod"},
		{"version wins over alias", Destination{FunctionName: "fn", Version: "fn-v1", Alias: "prod"}, "fn:fn-v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.dest.functionRouteName())
		})
	}
}

func TestAllowedHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/plain")
	h.Set("X-Request-Id", "abc")
	h.Add("X-Multi", "a")
	h.Add("X-Multi", "b")
	h.Set("X-Fission-Invoke-Mode", "async") // internal control header: dropped
	h.Set("Authorization", "Bearer secret") // caller session: dropped
	h.Set("Host", "example.com")            // not X-*: dropped
	h.Set("Cookie", "s=1")                  // caller session: dropped

	got := allowedHeaders(h)
	assert.Equal(t, "application/json", got["Content-Type"])
	assert.Equal(t, "text/plain", got["Accept"])
	assert.Equal(t, "abc", got["X-Request-Id"])
	assert.Equal(t, "a,b", got["X-Multi"], "multi-valued headers comma-joined")
	assert.NotContains(t, got, "X-Fission-Invoke-Mode")
	assert.NotContains(t, got, "Authorization")
	assert.NotContains(t, got, "Host")
	assert.NotContains(t, got, "Cookie")
}

func TestAllowedHeadersEmptyReturnsNil(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("Authorization", "x")
	require.Nil(t, allowedHeaders(h), "no replayable headers → nil, not empty map")
}

// FuzzEnvelopeDecode asserts Decode never panics on arbitrary bytes and that any
// successfully-decoded envelope re-encodes and re-decodes stably (round-trip).
func FuzzEnvelopeDecode(f *testing.F) {
	seed, _ := Envelope{Version: "1.0", Namespace: "ns", Function: "fn", Method: "POST", Body: []byte("hi")}.Encode()
	f.Add(seed)
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := Decode(data)
		if err != nil {
			return // arbitrary bytes may fail to decode; the requirement is no panic
		}
		out, err := env.Encode()
		require.NoError(t, err)
		out2, err := Decode(out)
		require.NoError(t, err)
		re, err := out2.Encode()
		require.NoError(t, err)
		require.True(t, bytes.Equal(out, re), "round-trip must be stable")
	})
}
