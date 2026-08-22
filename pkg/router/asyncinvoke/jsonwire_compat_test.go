// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is a compat gate for the json/v2 migration -- queued envelopes
// from the previous release must keep decoding, and new encodes must stay
// decodable by the previous release; if this fails after a migration commit,
// add compat options at the site, do not update the fixture.
//
// Each fixture below is a hand-written, byte-verified JSON literal pinning
// today's (encoding/json v1) Encode output for Envelope and ResultEnvelope,
// the two wire shapes that outlive rolling upgrades in a statestore Queue
// message body (see envelope.go's package doc). Comparisons are on the raw
// string, not JSONEq, because the migration risk is byte-level emission of
// null/[]/omitted fields -- exactly what JSONEq's semantic comparison would
// paper over.

// jsonWireFullEnvelope is today's v1 Encode() output for a fully-populated
// Envelope: every field set, Policy non-zero, both destinations present.
// The query value's ampersand is emitted as the six-character escape
// sequence backslash-u-0-0-2-6 -- v1's json.Marshal HTML-escapes &, <, and >
// by default (SetEscapeHTML(false) is opt-in), so this fixture pins that
// escaping too.
const jsonWireFullEnvelope = `{"version":"1.0","namespace":"ns1","function":"fn1","functionVersion":"fn1-v2","method":"POST","path":"/x/y","query":"a=1\u0026b=2","headers":{"Content-Type":"application/json"},"body":"aGVsbG8tYm9keQ==","enqueueTime":"2024-01-15T10:30:00Z","depth":1,"functionTimeout":30,"policy":{"maxAttempts":5,"backoffBase":2000000000,"backoffCap":60000000000,"maxAge":3600000000000,"noJitter":true},"onSuccess":{"fnNs":"ns1","fn":"next","alias":"prod"},"onFailure":{"fnNs":"ns1","topic":"dlq-topic","mqType":"kafka"}}`

func jsonWireFullEnvelopeValue() Envelope {
	return Envelope{
		Version:         "1.0",
		Namespace:       "ns1",
		Function:        "fn1",
		FunctionVersion: "fn1-v2",
		Method:          "POST",
		Path:            "/x/y",
		Query:           "a=1&b=2",
		Headers:         map[string]string{"Content-Type": "application/json"},
		Body:            []byte("hello-body"),
		EnqueueTime:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		Depth:           1,
		FunctionTimeout: 30,
		Policy: Policy{
			MaxAttempts: 5,
			BackoffBase: 2 * time.Second,
			BackoffCap:  time.Minute,
			MaxAge:      time.Hour,
			NoJitter:    true,
		},
		OnSuccess: &Destination{FunctionNamespace: "ns1", FunctionName: "next", Alias: "prod"},
		OnFailure: &Destination{FunctionNamespace: "ns1", Topic: "dlq-topic", MQType: "kafka"},
	}
}

// TestJSONWireCompat_EnvelopeFullyPopulated pins the v1 byte encoding of a
// fully-populated Envelope, and that the previous release's bytes still
// decode to the identical struct today.
func TestJSONWireCompat_EnvelopeFullyPopulated(t *testing.T) {
	t.Parallel()
	env := jsonWireFullEnvelopeValue()

	got, err := env.Encode()
	require.NoError(t, err)
	require.Equal(t, jsonWireFullEnvelope, string(got), "today's Encode output must match the pinned v1 wire fixture byte-for-byte")

	decoded, err := Decode([]byte(jsonWireFullEnvelope))
	require.NoError(t, err)
	assert.Equal(t, env, decoded, "the pinned fixture must decode back to the identical struct")
}

// jsonWireZeroEnvelope is today's v1 Encode() output for a zero-heavy
// Envelope: nil Headers/Body, zero Policy (omitzero), nil destinations, empty
// Path/Query, empty FunctionVersion. This pins today's null-vs-absent
// emission -- every omitempty/omitzero field is entirely ABSENT from the
// object, never emitted as `null`, `""`, `[]`, or `{}` -- which a json/v2
// migration could change (v2's omitzero/omitempty semantics for "empty" are
// stricter/different from v1's).
const jsonWireZeroEnvelope = `{"version":"1.0","namespace":"ns2","function":"fn2","method":"GET","enqueueTime":"2024-01-15T10:30:00Z","depth":0,"functionTimeout":0}`

func jsonWireZeroEnvelopeValue() Envelope {
	return Envelope{
		Version:     "1.0",
		Namespace:   "ns2",
		Function:    "fn2",
		Method:      "GET",
		EnqueueTime: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		// Depth and FunctionTimeout are int with no omitempty tag, so they
		// emit as 0 rather than being dropped -- see the fixture above.
	}
}

// TestJSONWireCompat_EnvelopeZeroHeavy pins the v1 byte encoding of a
// zero-heavy Envelope (nil slices/maps, zero Policy). This is the case most
// likely to regress on a json/v2 migration: v1's omitempty drops nil
// maps/slices and the Policy struct's `omitzero` tag drops it entirely when
// every field is zero; a differently-behaved v2 encoder could instead emit
// `null`, `{}`, or `[]` for these, which would still be semantically
// equivalent JSON but is a byte-level wire change for messages already
// queued by the previous release.
func TestJSONWireCompat_EnvelopeZeroHeavy(t *testing.T) {
	t.Parallel()
	env := jsonWireZeroEnvelopeValue()

	got, err := env.Encode()
	require.NoError(t, err)
	require.Equal(t, jsonWireZeroEnvelope, string(got), "today's Encode output must match the pinned v1 wire fixture byte-for-byte")

	decoded, err := Decode([]byte(jsonWireZeroEnvelope))
	require.NoError(t, err)
	assert.Equal(t, env, decoded, "the pinned fixture must decode back to the identical (zero-heavy) struct")
	assert.Nil(t, decoded.Headers, "absent headers decode to nil, not an empty map")
	assert.Nil(t, decoded.Body, "absent body decodes to nil, not an empty slice")
	assert.Nil(t, decoded.OnSuccess)
	assert.Nil(t, decoded.OnFailure)
	assert.Equal(t, Policy{}, decoded.Policy, "absent policy decodes to the zero Policy")
}

// jsonWireResultEnvelope is today's v1 Encode() output for a fully-populated
// ResultEnvelope.
const jsonWireResultEnvelope = `{"version":"1.0","requestContext":{"invocationId":"inv-1","functionRef":"ns1/fn1","condition":"Success","attempts":3,"depth":1},"requestPayload":"cmVxLWJvZHk=","responseContext":{"statusCode":200},"responsePayload":"cmVzcC1ib2R5"}`

func jsonWireResultEnvelopeValue() ResultEnvelope {
	return ResultEnvelope{
		Version: "1.0",
		RequestContext: RequestContext{
			InvocationID: "inv-1",
			FunctionRef:  "ns1/fn1",
			Condition:    ConditionSuccess,
			Attempts:     3,
			Depth:        1,
		},
		RequestPayload:  []byte("req-body"),
		ResponseContext: ResponseContext{StatusCode: 200},
		ResponsePayload: []byte("resp-body"),
	}
}

// TestJSONWireCompat_ResultEnvelope pins the v1 byte encoding of a
// ResultEnvelope, and that the previous release's bytes still decode to the
// identical struct today. ResultEnvelope has no dedicated Decode function
// (see envelope.go), so the decode path here mirrors the caller -- a
// destination invocation body -- which unmarshals directly with
// encoding/json, same as TestResultEnvelopeRoundTrip.
func TestJSONWireCompat_ResultEnvelope(t *testing.T) {
	t.Parallel()
	re := jsonWireResultEnvelopeValue()

	got, err := re.Encode()
	require.NoError(t, err)
	require.Equal(t, jsonWireResultEnvelope, string(got), "today's Encode output must match the pinned v1 wire fixture byte-for-byte")

	var decoded ResultEnvelope
	require.NoError(t, json.Unmarshal([]byte(jsonWireResultEnvelope), &decoded))
	assert.Equal(t, re, decoded, "the pinned fixture must decode back to the identical struct")
}
