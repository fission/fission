// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/memory"
)

// brokerStub records published jobs and fails the first failN calls.
type brokerStub struct {
	mu    sync.Mutex
	jobs  []mqpub.EgressJob
	failN int
}

func (b *brokerStub) publish(_ context.Context, job mqpub.EgressJob) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failN > 0 {
		b.failN--
		return errors.New("broker down")
	}
	b.jobs = append(b.jobs, job)
	return nil
}

func (b *brokerStub) published() []mqpub.EgressJob {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]mqpub.EgressJob(nil), b.jobs...)
}

func newTestConsumer(t *testing.T, broker *brokerStub) (*Consumer, statestore.Queue) {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	q, err := caps.Queue()
	require.NoError(t, err)
	c := New(logr.Discard(), q, fv1.MessageQueueTypeKafka, broker.publish)
	c.poll = 5 * time.Millisecond
	c.backoff = 20 * time.Millisecond
	return c, q
}

func startConsumer(t *testing.T, c *Consumer) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func enqueueJob(t *testing.T, q statestore.Queue, topic, payload string) {
	t.Helper()
	pub := mqpub.NewEgressPublisher(q, fv1.MessageQueueTypeKafka)
	require.NoError(t, pub.Publish(t.Context(), "ns", fv1.MessageQueueTypeKafka, topic, "text/plain", []byte(payload)))
}

func TestConsumerPublishesAndSettles(t *testing.T) {
	t.Parallel()
	broker := &brokerStub{}
	c, q := newTestConsumer(t, broker)
	startConsumer(t, c)

	enqueueJob(t, q, "orders", "one")
	enqueueJob(t, q, "orders", "two")

	require.Eventually(t, func() bool { return len(broker.published()) == 2 }, 5*time.Second, 10*time.Millisecond)
	got := broker.published()
	assert.Equal(t, "ns", got[0].Namespace)
	assert.Equal(t, "orders", got[0].Topic)

	// Acked: the queue drains (nothing redelivers after the lease window).
	require.Eventually(t, func() bool {
		stats, err := q.Stats(t.Context(), c.queue)
		return err == nil && stats.Visible == 0 && stats.Leased == 0
	}, 5*time.Second, 10*time.Millisecond, "acked jobs leave the queue")
}

func TestConsumerRetriesThenPublishes(t *testing.T) {
	t.Parallel()
	broker := &brokerStub{failN: 1}
	c, q := newTestConsumer(t, broker)
	startConsumer(t, c)

	enqueueJob(t, q, "orders", "flaky")
	require.Eventually(t, func() bool { return len(broker.published()) == 1 }, 5*time.Second, 10*time.Millisecond,
		"a failed broker publish is retried per the queue budget, not dropped (E4)")
}

func TestConsumerKillsMalformedJob(t *testing.T) {
	t.Parallel()
	broker := &brokerStub{}
	c, q := newTestConsumer(t, broker)
	startConsumer(t, c)

	_, err := q.Enqueue(t.Context(), c.queue, statestore.Message{Body: []byte("not json{")}, statestore.EnqueueOptions{})
	require.NoError(t, err)

	// The poison job dead-letters immediately (retries cannot fix the bytes)
	// and stays inspectable.
	require.Eventually(t, func() bool {
		dead, derr := q.DeadLetters(t.Context(), c.queue, statestore.Page{})
		return derr == nil && len(dead) == 1
	}, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, broker.published(), "nothing was published for the malformed job")
}
