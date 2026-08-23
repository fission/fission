// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqpub

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEgressJobJSONWireCompat is a compat gate for the json/v2 migration —
// this emission is a durable/cross-version contract: EgressJob is JSON-encoded
// (egress.go's Publish, ~line 76) onto a statestore queue where it outlives
// rolling upgrades, so an old consumer's decoder must still understand what a
// new producer writes and vice versa. If this fails after a migration commit,
// add compat options at the marshal site in egress.go, do not update the
// fixture.
func TestEgressJobJSONWireCompat(t *testing.T) {
	t.Parallel()

	t.Run("fully populated", func(t *testing.T) {
		t.Parallel()

		job := EgressJob{
			Namespace:   "default",
			Topic:       "orders.created",
			ContentType: "application/json",
			Payload:     []byte("hello world"),
		}
		const want = `{"namespace":"default","topic":"orders.created","contentType":"application/json","payload":"aGVsbG8gd29ybGQ="}`

		got, err := json.Marshal(job, egressWireOpts)
		require.NoError(t, err)
		require.Equal(t, want, string(got))

		var back EgressJob
		require.NoError(t, json.Unmarshal(got, &back))
		assert.Equal(t, job.Namespace, back.Namespace)
		assert.Equal(t, job.Topic, back.Topic)
		assert.Equal(t, job.ContentType, back.ContentType)
		assert.Equal(t, job.Payload, back.Payload)
	})

	t.Run("zero-heavy nil payload", func(t *testing.T) {
		t.Parallel()

		// Zero value: ContentType is omitempty (omitted), Payload is a nil
		// []byte with NO omitempty tag — today's v1 encoder still emits it as
		// the JSON literal null (not "" and not omitted). Pin that exact
		// shape: json/v2 changes to []byte/omitempty semantics would flip
		// this to omitted or "".
		job := EgressJob{}
		const want = `{"namespace":"","topic":"","payload":null}`

		got, err := json.Marshal(job, egressWireOpts)
		require.NoError(t, err)
		require.Equal(t, want, string(got))

		var back EgressJob
		require.NoError(t, json.Unmarshal(got, &back))
		assert.Equal(t, job.Namespace, back.Namespace)
		assert.Equal(t, job.Topic, back.Topic)
		assert.Equal(t, job.ContentType, back.ContentType)
		assert.Nil(t, back.Payload)
	})
}
