// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

const (
	// timerQueue holds Queue-backed backoff delays (and phase-4 Wait
	// states): durable, so a pod death never loses a pending retry.
	timerQueue = "wf-timers"

	timerBatch        = 16
	timerLease        = 30 * time.Second
	timerPollInterval = time.Second
	timerRetryOnErr   = 5 * time.Second
)

// timerMsg is one armed delay.
type timerMsg struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	State     string `json:"state"`
	Attempt   int32  `json:"attempt"`
}

// TimerLoop turns fired wf-timers messages into CAS-appended TimerFired
// events. Idempotent by the same guard the invoker uses: a duplicate
// delivery or a raced terminal just Acks. Safe to run on multiple replicas
// (leases serialize deliveries).
func (e *Engine) TimerLoop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if n := e.timerPollOnce(ctx); n == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(timerPollInterval):
			}
		}
	}
}

func (e *Engine) timerPollOnce(ctx context.Context) int {
	msgs, err := e.q.Lease(ctx, timerQueue, timerBatch, timerLease)
	if err != nil {
		e.logger.Error(err, "leasing timers")
		return 0
	}
	for _, msg := range msgs {
		var tm timerMsg
		if err := json.Unmarshal(msg.Body, &tm); err != nil {
			// Undecodable = never processable: settle it away.
			e.logger.Error(err, "dropping undecodable timer message", "id", msg.ID)
			_ = e.q.Kill(ctx, msg.Receipt, "undecodable timer message")
			continue
		}

		// A redelivery whose predecessor already appended (the Ack raced a
		// lease expiry) can land a duplicate TimerFired when nothing else
		// wrote in between: harmless — the fold's TimersFired set is
		// idempotent and no W-invariant covers timer events.
		ev := Event{Type: EvTimerFired, State: tm.State, Attempt: tm.Attempt}
		stream := "wfrun/" + tm.UID
		head, err := e.el.Head(ctx, stream)
		if err != nil {
			_ = e.q.Nack(ctx, msg.Receipt, timerRetryOnErr)
			continue
		}
		err = appendGuarded(ctx, e.el, stream, head, ev, func(raced Event) bool {
			switch raced.Type {
			case EvTimerFired:
				return raced.State == tm.State && raced.Attempt == tm.Attempt
			case EvRunSucceeded, EvRunFailed, EvRunCancelled, EvRunTimedOut:
				return true
			default:
				return false
			}
		})
		if err != nil {
			_ = e.q.Nack(ctx, msg.Receipt, timerRetryOnErr)
			continue
		}
		if err := e.q.Ack(ctx, msg.Receipt); err != nil {
			e.logger.V(1).Info("timer ack raced a newer lease (expected)", "id", msg.ID, "error", err)
		}
		e.wake(types.NamespacedName{Namespace: tm.Namespace, Name: tm.Name})
	}
	return len(msgs)
}
