// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/memory"
)

// received is one delivery the scripted function endpoint captured.
type received struct {
	Body        string
	ContentType string
	Topic       string
	RespTopic   string
}

// fnEndpoint is a scripted stand-in for the router-internal function URL.
type fnEndpoint struct {
	mu     sync.Mutex
	got    []received
	status int    // response status (default 200)
	body   string // response body
	failN  int    // fail the first N requests with 500
}

func (f *fnEndpoint) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, received{
		Body:        string(b),
		ContentType: r.Header.Get("Content-Type"),
		Topic:       r.Header.Get("X-Fission-MQTrigger-Topic"),
		RespTopic:   r.Header.Get("X-Fission-MQTrigger-RespTopic"),
	})
	n := len(f.got)
	failN, status, body := f.failN, f.status, f.body
	f.mu.Unlock()
	if n <= failN {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fnEndpoint) deliveries() []received {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]received(nil), f.got...)
}

// newTestProvider builds a Statestore over the memory driver pointed at the
// scripted endpoint (its URL stands in for the router internal listener).
func newTestProvider(t *testing.T, routerURL string) *Statestore {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	el, err := caps.EventLog()
	require.NoError(t, err)
	kv, err := caps.KV()
	require.NoError(t, err)
	logger := logr.Discard()
	if testing.Verbose() {
		logger = funcr.New(func(prefix, args string) { t.Log(prefix, args) }, funcr.Options{Verbosity: 2})
	}
	return &Statestore{
		logger:          logger,
		routerURL:       routerURL,
		caps:            caps,
		el:              el,
		kv:              kv,
		pub:             mqpub.NewStatestorePublisher(el),
		client:          http.DefaultClient,
		subs:            newSubscriptionSet(),
		reaperStop:      make(chan struct{}),
		reaperMaxAge:    maxStreamAge,
		reaperMaxEvents: maxStreamEvents,
		pollOverride:    10 * time.Millisecond,
	}
}

func testTrigger(name, topic string, mutate func(*fv1.MessageQueueTrigger)) *fv1.MessageQueueTrigger {
	tr := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
			MessageQueueType:  fv1.MessageQueueTypeStatestore,
			Topic:             topic,
			MaxRetries:        1,
			ContentType:       "application/json",
		},
	}
	if mutate != nil {
		mutate(tr)
	}
	return tr
}

// startSub subscribes and WAITS for the loop to establish its start-at-head
// cursor before returning: Subscribe's goroutine snapshots the head
// asynchronously, so a publish racing that snapshot is legitimately "before the
// subscription" — tests must sequence after startup to be deterministic.
func startSub(t *testing.T, s *Statestore, trigger *fv1.MessageQueueTrigger) *subscription {
	t.Helper()
	msub, err := s.Subscribe(t.Context(), trigger)
	require.NoError(t, err)
	sub := msub.(*subscription)
	t.Cleanup(func() { _ = sub.Stop() })
	require.Eventually(t, sub.started.Load, 5*time.Second, 5*time.Millisecond, "subscription must establish its cursor")
	return sub
}

func publish(t *testing.T, s *Statestore, topic, contentType, payload string) {
	t.Helper()
	require.NoError(t, s.pub.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, topic, contentType, []byte(payload)))
}

func TestSubscriptionDeliversInOrder(t *testing.T) {
	t.Parallel()
	fn := &fnEndpoint{}
	srv := httptest.NewServer(http.HandlerFunc(fn.handler))
	defer srv.Close()
	s := newTestProvider(t, srv.URL)

	sub := startSub(t, s, testTrigger("t1", "orders", nil))

	publish(t, s, "orders", "application/json", `{"n":1}`)
	publish(t, s, "orders", "text/plain", "two")

	require.Eventually(t, func() bool { return len(fn.deliveries()) == 2 }, 5*time.Second, 10*time.Millisecond)
	got := fn.deliveries()
	assert.Equal(t, `{"n":1}`, got[0].Body)
	assert.Equal(t, "application/json", got[0].ContentType, "the published contentType is replayed")
	assert.Equal(t, "orders", got[0].Topic)
	assert.Equal(t, "two", got[1].Body)
	assert.Equal(t, "text/plain", got[1].ContentType)

	// The cursor durably reaches the last delivered seq.
	require.Eventually(t, func() bool { return sub.committed.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
}

func TestSubscriptionStartsAtHead(t *testing.T) {
	t.Parallel()
	fn := &fnEndpoint{}
	srv := httptest.NewServer(http.HandlerFunc(fn.handler))
	defer srv.Close()
	s := newTestProvider(t, srv.URL)

	// Published BEFORE the subscription exists: not delivered (start-at-head).
	publish(t, s, "orders", "", "before")

	startSub(t, s, testTrigger("t1", "orders", nil))
	publish(t, s, "orders", "", "after")

	require.Eventually(t, func() bool { return len(fn.deliveries()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "after", fn.deliveries()[0].Body)
	// Give the loop a beat: "before" must never arrive.
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, fn.deliveries(), 1)
}

func TestSubscriptionPoisonToErrorTopicAndContinues(t *testing.T) {
	t.Parallel()
	fn := &fnEndpoint{failN: 1 << 30} // every delivery fails
	srv := httptest.NewServer(http.HandlerFunc(fn.handler))
	defer srv.Close()
	s := newTestProvider(t, srv.URL)

	sub := startSub(t, s, testTrigger("t1", "orders", func(tr *fv1.MessageQueueTrigger) {
		tr.Spec.MaxRetries = 1
		tr.Spec.ErrorTopic = "orders-errors"
	}))

	publish(t, s, "orders", "application/json", "poison")

	// The poison event lands on the error topic (E5) and the cursor advances.
	errStream := mqpub.StreamForTopic("ns", "orders-errors")
	require.Eventually(t, func() bool {
		evs, err := s.el.Read(t.Context(), errStream, 0, 10)
		return err == nil && len(evs) == 1
	}, 5*time.Second, 10*time.Millisecond)
	evs, err := s.el.Read(t.Context(), errStream, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, []byte("poison"), evs[0].Payload, "the ORIGINAL payload is routed to the error topic")
	require.Eventually(t, func() bool { return sub.committed.Load() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the cursor advances past the poison event — one bad event cannot wedge the topic")

	// MaxRetries=1 → exactly 2 attempts were made.
	assert.Len(t, fn.deliveries(), 2)
}

func TestSubscriptionResponseTopic(t *testing.T) {
	t.Parallel()
	fn := &fnEndpoint{body: "fn-response"}
	srv := httptest.NewServer(http.HandlerFunc(fn.handler))
	defer srv.Close()
	s := newTestProvider(t, srv.URL)

	startSub(t, s, testTrigger("t1", "orders", func(tr *fv1.MessageQueueTrigger) {
		tr.Spec.ResponseTopic = "orders-replies"
	}))
	publish(t, s, "orders", "", "req")

	respStream := mqpub.StreamForTopic("ns", "orders-replies")
	require.Eventually(t, func() bool {
		evs, err := s.el.Read(t.Context(), respStream, 0, 10)
		return err == nil && len(evs) == 1
	}, 5*time.Second, 10*time.Millisecond)
	evs, err := s.el.Read(t.Context(), respStream, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, []byte("fn-response"), evs[0].Payload)
	assert.Equal(t, "text/plain", evs[0].Type, "the function's response Content-Type travels")
}

func TestSubscriptionResumesFromDurableCursor(t *testing.T) {
	t.Parallel()
	fn := &fnEndpoint{}
	srv := httptest.NewServer(http.HandlerFunc(fn.handler))
	defer srv.Close()
	s := newTestProvider(t, srv.URL)
	trigger := testTrigger("t1", "orders", nil)

	sub := startSub(t, s, trigger)
	publish(t, s, "orders", "", "one")
	require.Eventually(t, func() bool { return sub.committed.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, sub.Stop())

	// Published while no consumer runs; the restart must resume from the durable
	// cursor — deliver "two", never redeliver "one".
	publish(t, s, "orders", "", "two")
	startSub(t, s, trigger)

	require.Eventually(t, func() bool { return len(fn.deliveries()) == 2 }, 5*time.Second, 10*time.Millisecond)
	got := fn.deliveries()
	assert.Equal(t, "one", got[0].Body)
	assert.Equal(t, "two", got[1].Body)
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, fn.deliveries(), 2, "no redelivery of already-committed events")
}

func TestReapStreamMinCursorAndBackstops(t *testing.T) {
	t.Parallel()
	s := newTestProvider(t, "http://unused")

	// 5 events on the stream; a subscriber committed through 3.
	for range 5 {
		publish(t, s, "orders", "", "x")
	}
	stream := mqpub.StreamForTopic("ns", "orders")

	// Min-cursor trim: events 1..3 go, 4..5 stay.
	s.reapStream(t.Context(), stream, 3)
	evs, err := s.el.Read(t.Context(), stream, 0, 10)
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.EqualValues(t, 4, evs[0].Seq)

	// Size backstop: cap of 1 retained event overrides a stalled cursor (0).
	s.reaperMaxEvents = 1
	s.reapStream(t.Context(), stream, 0)
	evs, err = s.el.Read(t.Context(), stream, 0, 10)
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.EqualValues(t, 5, evs[0].Seq)

	// Age backstop: everything is "old" with a zero max age.
	s.reaperMaxAge = -time.Hour
	s.reapStream(t.Context(), stream, 0)
	evs, err = s.el.Read(t.Context(), stream, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, evs, "age backstop trims events older than the cutoff")

	// Idempotent when nothing to trim.
	s.reapStream(t.Context(), stream, 0)
}

// TestByStreamBlocksUnstartedSubscriptions: a subscription that has not
// established its cursor yet has an unknown durable floor that may lie below
// every started sibling's — its stream must lose the loss-free min-cursor trim
// candidate entirely (invariant E3), regardless of map iteration order.
func TestByStreamBlocksUnstartedSubscriptions(t *testing.T) {
	t.Parallel()
	started := func(stream string, committed int64) *subscription {
		s := &subscription{stream: stream}
		s.committed.Store(committed)
		s.started.Store(true)
		return s
	}
	unstarted := func(stream string) *subscription { return &subscription{stream: stream} }

	ss := newSubscriptionSet()
	ss.add(started("topic/ns/a", 100))
	ss.add(unstarted("topic/ns/a"))
	ss.add(started("topic/ns/b", 7))

	got := ss.byStream()
	assert.EqualValues(t, noMinCursor, got["topic/ns/a"],
		"an unstarted subscription must block min-cursor trimming for its stream")
	assert.EqualValues(t, 7, got["topic/ns/b"], "unrelated streams keep their floor")

	// noMinCursor disables candidate 1 in reapStream: nothing may be trimmed.
	s := newTestProvider(t, "http://unused")
	for range 3 {
		publish(t, s, "orders", "", "x")
	}
	stream := mqpub.StreamForTopic("ns", "orders")
	s.reapStream(t.Context(), stream, noMinCursor)
	evs, err := s.el.Read(t.Context(), stream, 0, 10)
	require.NoError(t, err)
	assert.Len(t, evs, 3, "no loss-free trim while any floor is unknown")
}
