// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

func TestEventRoundTrip(t *testing.T) {
	t.Parallel()

	spec := &fv1.WorkflowSpec{
		StartAt: "a",
		States: map[string]fv1.WorkflowState{
			"a": {Type: fv1.WorkflowStateSucceed},
		},
	}

	events := []Event{
		{Type: EvRunStarted, Spec: spec, Input: json.RawMessage(`{"order":4711}`)},
		{Type: EvStepScheduled, State: "a", Attempt: 1, InputHash: "abc"},
		{Type: EvStepSucceeded, State: "a", Attempt: 1, Output: json.RawMessage(`{"ok":true}`)},
		{Type: EvStepFailed, State: "a", Attempt: 1, ErrorType: fv1.WorkflowErrFunctionError, Cause: json.RawMessage(`"boom"`)},
		{Type: EvTimerFired, State: "a", Attempt: 1},
		{Type: EvRunSucceeded, OutputRef: "io/final"},
		{Type: EvRunFailed, ErrorType: fv1.WorkflowErrTimeout},
		{Type: EvRunCancelled},
		{Type: EvRunTimedOut},
	}

	for _, ev := range events {
		t.Run(string(ev.Type), func(t *testing.T) {
			t.Parallel()
			se, err := encodeEvent(ev)
			require.NoError(t, err)
			assert.Equal(t, string(ev.Type), se.Type, "statestore envelope type mirrors the event type")

			got, err := decodeEvent(se)
			require.NoError(t, err)
			assert.Equal(t, ev, got)
		})
	}
}

func TestDecodeEventUnknownTypeFailsLoud(t *testing.T) {
	t.Parallel()

	_, err := decodeEvent(statestore.Event{Type: "SomethingNew", Payload: []byte(`{}`)})
	require.Error(t, err, "an unknown event type means a newer writer touched the stream; refuse to guess")

	_, err = decodeEvent(statestore.Event{Type: string(EvRunStarted), Payload: []byte(`{not json`)})
	require.Error(t, err)
}

func TestStreamName(t *testing.T) {
	t.Parallel()

	run := &fv1.WorkflowRun{ObjectMeta: metav1.ObjectMeta{
		Name: "r1", Namespace: "default", UID: types.UID("2fd0ad4d-9c6e-4a4f-8f7e-000000000001"),
	}}
	assert.Equal(t, "wfrun/2fd0ad4d-9c6e-4a4f-8f7e-000000000001", streamName(run))
}
