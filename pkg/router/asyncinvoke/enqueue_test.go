// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	// Register the in-memory driver for the enqueue tests.
	_ "github.com/fission/fission/pkg/statestore/memory"
)

func memQueue(t *testing.T) statestore.Queue {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	q, err := caps.Queue()
	require.NoError(t, err)
	return q
}

func TestEnqueueHappyPath(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	r := httptest.NewRequest("POST", "/fn/sub?x=1", strings.NewReader("payload"))
	r.Header.Set("Content-Type", "application/json")

	id, err := Enqueue(t.Context(), q, httptest.NewRecorder(), r, Params{
		Namespace: "ns", Function: "fn", FunctionTimeout: 30,
	})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	// Lease it back and verify the durable envelope: the id is the queue message
	// id, and the envelope faithfully reproduces the request.
	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	require.Equal(t, id, l[0].ID)
	env, err := Decode(l[0].Body)
	require.NoError(t, err)
	assert.Equal(t, EnvelopeVersion, env.Version)
	assert.Equal(t, "ns", env.Namespace)
	assert.Equal(t, "fn", env.Function)
	assert.Equal(t, "POST", env.Method)
	assert.Equal(t, "/fn/sub", env.Path)
	assert.Equal(t, "x=1", env.Query)
	assert.Equal(t, []byte("payload"), env.Body)
	assert.Equal(t, 30, env.FunctionTimeout)
	assert.Equal(t, "application/json", env.Headers["Content-Type"])
}

func TestEnqueueBodyTooLarge(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	r := httptest.NewRequest("POST", "/fn", strings.NewReader(strings.Repeat("a", 1024)))

	_, err := Enqueue(t.Context(), q, httptest.NewRecorder(), r, Params{
		Namespace: "ns", Function: "fn", MaxBodyBytes: 128,
	})
	require.ErrorIs(t, err, ErrBodyTooLarge)

	// A1: nothing was enqueued.
	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Empty(t, l)
}

func TestEnqueueMidBodyReadError(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	// A body that fails partway must produce an error, never a partial enqueue.
	r := httptest.NewRequest("POST", "/fn", iotest.ErrReader(errors.New("boom")))

	_, err := Enqueue(t.Context(), q, httptest.NewRecorder(), r, Params{Namespace: "ns", Function: "fn"})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBodyTooLarge)

	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Empty(t, l, "A1: a failed read must not leave a partial enqueue")
}

func TestEnqueueDedupCollapse(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	enqueue := func() (string, error) {
		r := httptest.NewRequest("POST", "/fn", strings.NewReader("x"))
		return Enqueue(t.Context(), q, httptest.NewRecorder(), r, Params{Namespace: "ns", Function: "fn", DedupKey: "k"})
	}
	id1, err := enqueue()
	require.NoError(t, err)
	id2, err := enqueue()
	require.NoError(t, err)
	require.Equal(t, id1, id2, "same dedup key collapses to the same invocation id")
}
