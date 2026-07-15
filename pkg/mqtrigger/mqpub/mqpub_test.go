// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqpub

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/memory"
)

func memEventLog(t *testing.T) statestore.EventLog {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	el, err := caps.EventLog()
	require.NoError(t, err)
	return el
}

func TestStreamForTopic(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "topic/ns/orders", StreamForTopic("ns", "orders"))
}

func TestStatestorePublisherPublish(t *testing.T) {
	t.Parallel()
	el := memEventLog(t)
	p := NewStatestorePublisher(el)

	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "orders", "application/json", []byte(`{"ok":true}`)))
	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "orders", "text/plain", []byte("second")))

	// Events land in the namespaced stream, ordered, with contentType as Type.
	evs, err := el.Read(t.Context(), StreamForTopic("ns", "orders"), 0, 0)
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.Equal(t, "application/json", evs[0].Type)
	assert.Equal(t, []byte(`{"ok":true}`), evs[0].Payload)
	assert.Equal(t, "text/plain", evs[1].Type)
	assert.EqualValues(t, 1, evs[0].Seq)
	assert.EqualValues(t, 2, evs[1].Seq)

	// Namespace isolation: another namespace's same-named topic is a different stream.
	require.NoError(t, p.Publish(t.Context(), "other", fv1.MessageQueueTypeStatestore, "orders", "text/plain", []byte("x")))
	evs, err = el.Read(t.Context(), StreamForTopic("other", "orders"), 0, 0)
	require.NoError(t, err)
	require.Len(t, evs, 1)
}

func TestStatestorePublisherUnsupportedType(t *testing.T) {
	t.Parallel()
	p := NewStatestorePublisher(memEventLog(t))
	err := p.Publish(t.Context(), "ns", "kafka", "orders", "application/json", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedMQType)
}

func TestStatestorePublisherRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	p := NewStatestorePublisher(memEventLog(t))
	// Sink-side defense in depth: inputs that would break the stream-name
	// injectivity are rejected even though admission already validates them.
	for name, call := range map[string]func() error{
		"empty namespace": func() error { return p.Publish(t.Context(), "", fv1.MessageQueueTypeStatestore, "t", "", nil) },
		"slash namespace": func() error { return p.Publish(t.Context(), "a/b", fv1.MessageQueueTypeStatestore, "t", "", nil) },
		"empty topic":     func() error { return p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "", "", nil) },
		"slash topic":     func() error { return p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "a/b", "", nil) },
		"space topic":     func() error { return p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "a b", "", nil) },
	} {
		assert.Errorf(t, call(), "%s must be rejected at the sink", name)
	}
}

func TestStatestorePublisherBacklogCap(t *testing.T) {
	t.Parallel()
	el := memEventLog(t)
	p := &statestorePublisher{el: el, maxBacklog: 2}

	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "t", "", []byte("1")))
	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "t", "", []byte("2")))
	err := p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "t", "", []byte("3"))
	require.Error(t, err, "a topic at the backlog cap rejects loudly, never grows silently")
	assert.ErrorIs(t, err, ErrTopicBacklogCap)

	// Other topics are unaffected (the cap is per-stream).
	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "other", "", []byte("x")))

	// The cap is on the RETAINED backlog: retention trimming the stream lets
	// publishes flow again (a consumed topic never bricks).
	require.NoError(t, el.Trim(t.Context(), StreamForTopic("ns", "t"), 3))
	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "t", "", []byte("4")))
}

// erroringEventLog fails every Append, to prove E1: a failed publish surfaces as
// an error, never a silent accept.
type erroringEventLog struct{ statestore.EventLog }

func (erroringEventLog) Append(context.Context, string, int64, []statestore.Event) (int64, error) {
	return 0, errors.New("store down")
}

func (erroringEventLog) Head(context.Context, string) (int64, error) { return 0, nil }

func TestStatestorePublisherE1FailsLoud(t *testing.T) {
	t.Parallel()
	p := NewStatestorePublisher(erroringEventLog{})
	err := p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "orders", "application/json", []byte("x"))
	require.Error(t, err, "a failed append must surface (E1), never a fake accept")
	assert.Contains(t, err.Error(), "ns/orders")
}
