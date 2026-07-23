// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils"

	"github.com/go-logr/logr"
)

// cursorScope returns the KV scope of a trigger's durable cursor, per the house
// Scope convention (Owner = <kind>/<name>).
func cursorScope(trigger *fv1.MessageQueueTrigger) statestore.Scope {
	return statestore.Scope{
		Namespace: trigger.Namespace,
		Owner:     "messagequeuetrigger/" + trigger.Name,
		Keyspace:  "cursor",
	}
}

const cursorKey = "cursor"

// subscription is one trigger's consumer loop over its topic stream: read from
// the durable cursor, deliver in order, publish ResponseTopic/ErrorTopic, and
// CAS-advance the cursor (the eventlogsub.tla protocol).
type subscription struct {
	logger  logr.Logger
	s       *Statestore
	trigger *fv1.MessageQueueTrigger
	stream  string
	fnURL   string

	// committed is the last CAS-persisted cursor, read by the retention reaper
	// (trim never passes it — invariant E3).
	committed atomic.Int64
	// started flips once the first cursor load/init succeeded; the reaper skips
	// subscriptions that have not established their floor yet.
	started atomic.Bool

	// poll paces idle reads and error retries; from the trigger's
	// PollingInterval (seconds) or defaultPollInterval (a field so tests can
	// tighten it).
	poll time.Duration

	cancel context.CancelFunc
	done   chan struct{}
}

func newSubscription(s *Statestore, trigger *fv1.MessageQueueTrigger) *subscription {
	poll := defaultPollInterval
	if p := trigger.Spec.PollingInterval; p != nil && *p > 0 {
		poll = time.Duration(*p) * time.Second
	}
	if s.pollOverride > 0 {
		poll = s.pollOverride
	}
	// RFC-0025: append the alias/version suffix when the reference carries
	// one; resolution stays entirely router-side.
	return &subscription{
		logger:  s.logger.WithName(trigger.Name),
		s:       s,
		trigger: trigger,
		stream:  mqpub.StreamForTopic(trigger.Namespace, trigger.Spec.Topic),
		fnURL:   s.routerURL + "/" + strings.TrimPrefix(utils.UrlForFunctionReference(trigger.Spec.FunctionReference, trigger.Namespace), "/"),
		poll:    poll,
		done:    make(chan struct{}),
	}
}

// Stop implements messageQueue.Subscription.
func (sub *subscription) Stop() error {
	if sub.cancel != nil {
		sub.cancel()
	}
	<-sub.done
	return nil
}

// Done implements messageQueue.Subscription.
func (sub *subscription) Done() <-chan struct{} { return sub.done }

// run is the consumer loop. It returns only when ctx is cancelled (trigger
// deleted, leadership lost, shutdown). Errors are logged and retried — a broken
// store or function must never kill the subscription.
func (sub *subscription) run(ctx context.Context) {
	cursor, version, err := sub.loadOrInitCursor(ctx)
	for err != nil {
		sub.logger.Error(err, "initializing topic cursor; retrying", "stream", sub.stream)
		if !sleepCtx(ctx, sub.poll) {
			return
		}
		cursor, version, err = sub.loadOrInitCursor(ctx)
	}
	sub.committed.Store(cursor)
	sub.started.Store(true)
	sub.logger.Info("topic subscription started", "stream", sub.stream, "cursor", cursor)

	for {
		if ctx.Err() != nil {
			return
		}
		events, err := sub.s.el.Read(ctx, sub.stream, cursor, readBatch)
		if err != nil {
			if ctx.Err() == nil {
				sub.logger.Error(err, "reading topic stream", "stream", sub.stream, "cursor", cursor)
			}
			if !sleepCtx(ctx, sub.poll) {
				return
			}
			continue
		}
		if len(events) == 0 {
			if !sleepCtx(ctx, sub.poll) {
				return
			}
			continue
		}
		// Gap detection: a first event above cursor+1 means retention trimmed
		// events this subscription never delivered (an age/size backstop
		// overriding a stalled floor). The loss already happened — surface it
		// HERE, at the subscriber that suffered it, or "my function missed
		// events" is undebuggable later.
		if gap := events[0].Seq - cursor - 1; gap > 0 {
			recordGap(ctx, gap)
			sub.logger.Error(nil, "topic events were trimmed before delivery to this subscription",
				"stream", sub.stream, "cursor", cursor, "resumedAt", events[0].Seq, "missed", gap)
		}
		progressed := false
		for _, ev := range events {
			if ctx.Err() != nil {
				break
			}
			if !sub.handle(ctx, ev) {
				// Terminal handling incomplete (e.g. the ErrorTopic publish
				// failed): do NOT advance past the event (E5) — re-read and
				// retry it after a pause.
				break
			}
			cursor = ev.Seq
			progressed = true
		}
		if progressed {
			cursor, version = sub.commit(ctx, cursor, version)
		} else if !sleepCtx(ctx, sub.poll) {
			return
		}
	}
}

// loadOrInitCursor reads the durable cursor, or initializes it at the stream
// head (start-at-head subscription) with a CAS create so racing instances
// converge on one record.
func (sub *subscription) loadOrInitCursor(ctx context.Context) (cursor, version int64, err error) {
	val, err := sub.s.kv.Get(ctx, cursorScope(sub.trigger), cursorKey)
	if err == nil {
		c, perr := strconv.ParseInt(string(val.Data), 10, 64)
		if perr != nil {
			// A corrupt cursor record is unrecoverable ambiguity: fail loud and
			// keep retrying rather than guessing a position.
			return 0, 0, fmt.Errorf("corrupt cursor record %q: %w", string(val.Data), perr)
		}
		return c, val.Version, nil
	}
	if !errors.Is(err, statestore.ErrNotFound) {
		return 0, 0, err
	}
	head, err := sub.s.el.Head(ctx, sub.stream)
	if err != nil {
		return 0, 0, err
	}
	// CAS create (absent key = version 0): the loser of a racing create re-reads
	// the winner's record.
	err = sub.s.kv.Set(ctx, cursorScope(sub.trigger), cursorKey,
		[]byte(strconv.FormatInt(head, 10)), statestore.SetOptions{IfVersion: new(int64(0))})
	if err != nil && !errors.Is(err, statestore.ErrVersionConflict) {
		return 0, 0, err
	}
	val, err = sub.s.kv.Get(ctx, cursorScope(sub.trigger), cursorKey)
	if err != nil {
		return 0, 0, err
	}
	c, perr := strconv.ParseInt(string(val.Data), 10, 64)
	if perr != nil {
		return 0, 0, fmt.Errorf("corrupt cursor record %q: %w", string(val.Data), perr)
	}
	return c, val.Version, nil
}

// commit CAS-persists the cursor. On a version conflict (an overlapping
// instance advanced it — leadership transition), it adopts the persisted record
// and resumes from there: the cursor never regresses (CursorMonotonic), and any
// re-read tail is at-least-once redelivery.
func (sub *subscription) commit(ctx context.Context, cursor, version int64) (int64, int64) {
	err := sub.s.kv.Set(ctx, cursorScope(sub.trigger), cursorKey,
		[]byte(strconv.FormatInt(cursor, 10)), statestore.SetOptions{IfVersion: new(version)})
	if err == nil {
		sub.committed.Store(cursor)
		return cursor, version + 1
	}
	if errors.Is(err, statestore.ErrVersionConflict) {
		if c, v, lerr := sub.loadOrInitCursor(ctx); lerr == nil {
			sub.logger.V(1).Info("cursor commit raced a newer instance; resuming from the persisted cursor",
				"local", cursor, "persisted", c)
			sub.committed.Store(c)
			return c, v
		}
	}
	if ctx.Err() == nil {
		sub.logger.Error(err, "persisting topic cursor; will retry after redelivery", "stream", sub.stream, "cursor", cursor)
	}
	// Keep the local position for this session; the next commit retries the CAS.
	return cursor, version
}

// handle delivers one event with the trigger's retry budget. It returns true
// when the event reached TERMINAL handling — delivered, or exhausted-and-
// error-published (E5) — and false when handling must be retried (the cursor
// must not advance).
func (sub *subscription) handle(ctx context.Context, ev statestore.Event) bool {
	ok := sub.deliver(ctx, ev)
	if ok {
		recordDelivery(ctx, "success")
		return true
	}
	if ctx.Err() != nil {
		return false // shutting down: leave the event for redelivery
	}
	// Poison isolation (E5): route to the ErrorTopic BEFORE advancing, so a
	// crash in between redelivers the event rather than skipping it. Without an
	// ErrorTopic the event is dropped-with-log (kafka-provider parity).
	// While the ErrorTopic publish keeps failing, each re-read re-runs the full
	// delivery budget against the (already failing) function — E5 deliberately
	// prices re-delivery below losing the event.
	if sub.trigger.Spec.ErrorTopic != "" {
		err := sub.s.pub.Publish(ctx, sub.trigger.Namespace, fv1.MessageQueueTypeStatestore,
			sub.trigger.Spec.ErrorTopic, ev.Type, ev.Payload)
		if err != nil {
			recordErrorTopic(ctx, "error")
			sub.logger.Error(err, "publishing exhausted event to error topic; will retry",
				"errorTopic", sub.trigger.Spec.ErrorTopic, "seq", ev.Seq)
			return false
		}
		recordErrorTopic(ctx, "published")
		sub.logger.Info("event exhausted retries; routed to error topic",
			"stream", sub.stream, "seq", ev.Seq, "errorTopic", sub.trigger.Spec.ErrorTopic,
			"maxRetries", sub.trigger.Spec.MaxRetries)
	} else {
		sub.logger.Error(nil, "event exhausted retries and no error topic is set; dropping",
			"stream", sub.stream, "seq", ev.Seq, "maxRetries", sub.trigger.Spec.MaxRetries)
		recordErrorTopic(ctx, "dropped")
	}
	// Counted only once terminal handling is secured: counting before the
	// ErrorTopic publish would inflate "exhausted" once per retry cycle while
	// that publish is broken — dashboards would lie mid-incident.
	recordDelivery(ctx, "exhausted")
	return true
}

// deliver POSTs the event to the function via the router internal listener,
// retrying up to MaxRetries. Success is any 2xx — a deliberate deviation from
// the kafka provider's strict 200, consistent with the RFC-0024 settle matrix.
// On success it best-effort publishes the response body to the ResponseTopic.
func (sub *subscription) deliver(ctx context.Context, ev statestore.Event) bool {
	contentType := ev.Type
	if contentType == "" {
		contentType = sub.trigger.Spec.ContentType
	}
	for attempt := 0; attempt <= sub.trigger.Spec.MaxRetries; attempt++ {
		if attempt > 0 {
			recordRetry(ctx)
			if !sleepCtx(ctx, retryBackoff) {
				return false
			}
		}
		// fnURL is validated at Subscribe time, so this branch is
		// defense-in-depth; continue treats it like any other failed attempt
		// rather than short-circuiting a "terminal" verdict the event never
		// earned (zero real attempts must not read as exhausted-and-error-
		// topic'd in the normal way retries do).
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.fnURL, bytes.NewReader(ev.Payload))
		if err != nil {
			sub.logger.Error(err, "building delivery request", "url", sub.fnURL, "seq", ev.Seq, "attempt", attempt)
			continue
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("X-Fission-MQTrigger-Topic", sub.trigger.Spec.Topic)
		req.Header.Set("X-Fission-MQTrigger-RespTopic", sub.trigger.Spec.ResponseTopic)
		req.Header.Set("X-Fission-MQTrigger-ErrorTopic", sub.trigger.Spec.ErrorTopic)

		resp, err := sub.s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			sub.logger.Error(err, "delivering topic event", "url", sub.fnURL, "seq", ev.Seq, "attempt", attempt)
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// Error level for kafka-provider parity: a function 500-ing on every
			// event must be visible in the head's logs at default verbosity, not
			// quieter than a connection failure.
			sub.logger.Error(nil, "topic delivery failed",
				"status", resp.StatusCode, "seq", ev.Seq, "attempt", attempt)
			continue
		}
		if rerr != nil {
			sub.logger.Error(rerr, "reading delivery response; response topic skipped", "seq", ev.Seq)
			return true // the function succeeded; the response read is best-effort
		}
		if sub.trigger.Spec.ResponseTopic != "" {
			respCT := resp.Header.Get("Content-Type")
			if err := sub.s.pub.Publish(ctx, sub.trigger.Namespace, fv1.MessageQueueTypeStatestore,
				sub.trigger.Spec.ResponseTopic, respCT, body); err != nil {
				recordResponseTopic(ctx, "error")
				sub.logger.Error(err, "publishing response to response topic (best-effort)",
					"responseTopic", sub.trigger.Spec.ResponseTopic, "seq", ev.Seq)
			} else {
				recordResponseTopic(ctx, "published")
			}
		}
		return true
	}
	return false
}

// sleepCtx sleeps d or returns false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
