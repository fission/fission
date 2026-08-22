// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/statestore"
)

// This file is a compat gate for the json/v2 migration, covering
// async_dlq.go's deliberate lenient two-type probe (dlqSummary, lines
// 272-289, mirrored by dlqWriteShow, lines 291-302): a dead-lettered
// message body is tried as an asyncinvoke.Envelope first and, only if that
// yields an empty Function, as an mqpub.EgressJob -- both via a plain
// json.Unmarshal (asyncinvoke.Decode is exactly that for Envelope), with no
// strict-decoding options. dlqSummary is called directly below rather than
// re-implemented, per the "call the actual probe function" instruction --
// it IS the probe (plus building the dlqMessage summary around it), so
// calling it exercises the real production code path, not a copy of it.
//
// If any of these classifications change after a json/v2 migration commit,
// add compat options at the site (e.g. explicit duplicate-key/invalid-UTF-8
// handling to preserve v1 leniency), do not update the fixture -- a changed
// classification for an already-dead-lettered message silently reclassifies
// it (namespace/function/topic swap in the admin API) with no migration
// path for the operator.

// dlqProbeEnvelopeBody is a payload that classifies as an asyncinvoke.Envelope
// today: Function is non-empty, so dlqSummary's first branch (async_dlq.go
// line 280) wins and returns before ever trying the EgressJob decode.
func dlqProbeEnvelopeBody(t *testing.T) []byte {
	t.Helper()
	body, err := asyncinvoke.Envelope{
		Version: asyncinvoke.EnvelopeVersion, Namespace: "ns-env", Function: "fn-env", Method: "POST",
	}.Encode()
	require.NoError(t, err)
	return body
}

// dlqProbeEgressJobBody is a payload that classifies as an mqpub.EgressJob
// today: it has no "function" key at all, so the Envelope decode succeeds
// but env.Function stays "" (the first branch's guard fails), falling
// through to the EgressJob branch (async_dlq.go line 285) where Topic is
// non-empty.
func dlqProbeEgressJobBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(mqpub.EgressJob{Namespace: "ns-egress", Topic: "topic-egress", Payload: []byte("x")})
	require.NoError(t, err)
	return body
}

// TestDLQProbeCompat_Classification pins today's classification outcome of
// async_dlq.go's dlqSummary two-type probe across the shapes it is meant to
// distinguish, plus the ambiguous/garbage cases where neither branch fires.
func TestDLQProbeCompat_Classification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		body          func(t *testing.T) []byte
		wantNamespace string
		wantFunction  string
		wantTopic     string
	}{
		{
			name:          "envelope with non-empty function classifies as an async invocation",
			body:          dlqProbeEnvelopeBody,
			wantNamespace: "ns-env",
			wantFunction:  "fn-env",
		},
		{
			name:          "egress job (no function key) classifies as a broker egress job",
			body:          dlqProbeEgressJobBody,
			wantNamespace: "ns-egress",
			wantTopic:     "topic-egress",
		},
		{
			name: "ambiguous valid JSON with neither discriminator classifies as neither",
			body: func(t *testing.T) []byte {
				t.Helper()
				return []byte(`{"foo":"bar","namespace":"ns-ambiguous"}`)
			},
			// Neither branch's guard fires (env.Function == "" and job.Topic ==
			// ""), so dlqSummary leaves Namespace/Function/Topic at their zero
			// value even though the body's own "namespace" key decoded fine --
			// the classification is all-or-nothing, not per-field.
		},
		{
			name: "garbage (invalid JSON) classifies as neither, no panic",
			body: func(t *testing.T) []byte {
				t.Helper()
				return []byte("not json at all { [ }")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dlqSummary(statestore.DeadMessage{ID: "id-" + tt.name, Body: tt.body(t)})
			assert.Equal(t, tt.wantNamespace, got.Namespace)
			assert.Equal(t, tt.wantFunction, got.Function)
			assert.Equal(t, tt.wantTopic, got.Topic)
		})
	}
}

// TestDLQProbeCompat_DuplicateKeys documents v1 leniency that a stricter
// json/v2 default (RejectDuplicateNames) would reject outright: a body with
// the "function" key repeated decodes without error, and the LAST occurrence
// wins (encoding/json's documented duplicate-key behavior), so the probe
// classifies the message under the second value.
func TestDLQProbeCompat_DuplicateKeys(t *testing.T) {
	t.Parallel()
	body := []byte(`{"version":"1.0","namespace":"ns-dup","function":"fn-dup-first","function":"fn-dup-second","method":"POST"}`)

	got := dlqSummary(statestore.DeadMessage{ID: "id-dup", Body: body})
	assert.Equal(t, "ns-dup", got.Namespace)
	assert.Equal(t, "fn-dup-second", got.Function, "v1 json.Unmarshal takes the last occurrence of a duplicate key")
	assert.Empty(t, got.Topic)

	// Pin the same leniency directly through asyncinvoke.Decode, since that
	// is the exact call the probe's first branch makes.
	env, err := asyncinvoke.Decode(body)
	require.NoError(t, err, "v1 does not reject duplicate keys")
	assert.Equal(t, "fn-dup-second", env.Function)
}

// TestDLQProbeCompat_InvalidUTF8 documents v1 leniency that a stricter
// json/v2 default would reject: a string value containing invalid UTF-8
// bytes decodes without error, with each invalid byte sequence silently
// replaced by the Unicode replacement character (U+FFFD) rather than
// producing a decode error. The replaced value is still non-empty, so the
// probe still classifies the message as an async invocation.
func TestDLQProbeCompat_InvalidUTF8(t *testing.T) {
	t.Parallel()
	// \xff\xfe are not valid UTF-8 on their own; embedded inside the
	// "function" string value they exercise json.Unmarshal's string-decoding
	// path rather than a top-level tokenizing failure.
	body := []byte("{\"version\":\"1.0\",\"namespace\":\"ns-badutf8\",\"function\":\"fn-\xff\xfe-bad\",\"method\":\"POST\"}")

	got := dlqSummary(statestore.DeadMessage{ID: "id-badutf8", Body: body})
	assert.Equal(t, "ns-badutf8", got.Namespace)
	assert.NotEmpty(t, got.Function, "invalid UTF-8 is replaced, not rejected, so Function stays non-empty and the envelope branch still wins")
	assert.Contains(t, got.Function, "�", "invalid bytes are replaced with U+FFFD")
	assert.Empty(t, got.Topic)

	env, err := asyncinvoke.Decode(body)
	require.NoError(t, err, "v1 does not reject invalid UTF-8 in string values")
	assert.Equal(t, "fn-��-bad", env.Function)
}
