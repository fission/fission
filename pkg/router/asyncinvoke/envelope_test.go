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
