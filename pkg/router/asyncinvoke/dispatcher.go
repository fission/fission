// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"context"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/statestore"
)

// Dead-letter reasons the dispatcher assigns (statestore.ReasonRetriesExhausted
// is the shared exhaustion reason).
const (
	// ReasonExpired dead-letters an invocation whose MaxAge elapsed before a
	// successful delivery.
	ReasonExpired = "expired"
	// ReasonHTTP4xx dead-letters an invocation a function rejected with a
	// permanent 4xx (a non-retryable client error, excluding 408/429).
	ReasonHTTP4xx = "http_4xx"
	// ReasonUndecodable dead-letters a message whose envelope will not decode — a
	// corrupt or wrong-version record no retry can fix.
	ReasonUndecodable = "undecodable_envelope"
)

// Dispatcher defaults.
const (
	DefaultMaxAttempts     = statestore.DefaultMaxAttempts
	DefaultBackoffBase     = 1 * time.Second
	DefaultBackoffCap      = 5 * time.Minute
	DefaultMaxAge          = 6 * time.Hour
	DefaultFunctionTimeout = 60 * time.Second
	DefaultLeaseDuration   = 5 * time.Minute
	DefaultBatchSize       = 10
	DefaultPollInterval    = 1 * time.Second

	// deliveryTimeoutBuffer extends a delivery's deadline past the function
	// timeout so a function running to its limit still completes, and is also the
	// gap kept below the lease so the delivery context always expires before the
	// lease (invariant A7): a slow-but-alive delivery is cancelled and retried,
	// never left to ack against a lease a newer delivery already owns.
	deliveryTimeoutBuffer = 10 * time.Second
)

// action is the settle decision for one delivery attempt.
type action int

const (
	actionAck   action = iota // 2xx: succeeded
	actionKill                // permanent 4xx: dead-letter, no retry
	actionRetry               // 5xx / 408 / 429 / transport error: nack with backoff
)

// classify maps a delivery outcome to a settle action (the RFC-0024 settle
// matrix). It is pure so the matrix is exhaustively property-tested.
func classify(res DeliveryResult) action {
	if res.Err != nil {
		return actionRetry // transport failure (dial error, timeout, cancel)
	}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return actionAck
	case res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusTooManyRequests:
		return actionRetry // 408 / 429 are transient
	case res.StatusCode >= 400 && res.StatusCode < 500:
		return actionKill // other 4xx: permanent client error
	default:
		return actionRetry // 5xx and anything unexpected
	}
}

// Options configures a Dispatcher. Queue, Deliverer, and Logger are required; the
// rest default. Now and Rand are injected so timing and backoff jitter are
// deterministic under test.
type Options struct {
	Queue     statestore.Queue
	Deliverer Deliverer
	Logger    logr.Logger

	QueueName     string        // "" → DefaultQueue
	BatchSize     int           // 0 → DefaultBatchSize
	PollInterval  time.Duration // 0 → DefaultPollInterval
	LeaseDuration time.Duration // 0 → DefaultLeaseDuration

	Now  func() time.Time // nil → time.Now
	Rand func() float64   // nil → rand/v2 Float64; returns [0,1) for backoff jitter
}

// Dispatcher leases async invocations from the statestore Queue, delivers each to
// the function's internal listener, and settles it (Ack / Nack-with-backoff /
// Kill) per the RFC-0024 settle matrix. Its lease/settle protocol is the shared
// docs/rfc/specs/queue.tla model (invariants A2/A3/A4 = I1/I2/I3), so a stale
// delivery can never decide a newer lease's outcome.
type Dispatcher struct {
	q         statestore.Queue
	deliverer Deliverer
	logger    logr.Logger

	queueName     string
	batchSize     int
	pollInterval  time.Duration
	leaseDuration time.Duration
	now           func() time.Time
	rand          func() float64
}

// New builds a Dispatcher from Options, applying defaults.
func New(opts Options) *Dispatcher {
	d := &Dispatcher{
		q:             opts.Queue,
		deliverer:     opts.Deliverer,
		logger:        opts.Logger,
		queueName:     opts.QueueName,
		batchSize:     opts.BatchSize,
		pollInterval:  opts.PollInterval,
		leaseDuration: opts.LeaseDuration,
		now:           opts.Now,
		rand:          opts.Rand,
	}
	if d.queueName == "" {
		d.queueName = DefaultQueue
	}
	if d.batchSize <= 0 {
		d.batchSize = DefaultBatchSize
	}
	if d.pollInterval <= 0 {
		d.pollInterval = DefaultPollInterval
	}
	if d.leaseDuration <= 0 {
		d.leaseDuration = DefaultLeaseDuration
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.rand == nil {
		d.rand = rand.Float64
	}
	return d
}

// Run leases and settles until ctx is cancelled: it leases a batch, delivers the
// batch concurrently, waits, and leases again; an empty lease sleeps pollInterval
// (interruptibly). Returns ctx.Err() on cancellation. Multiple router replicas
// call Run against the same queue safely — statestore leases are SKIP LOCKED.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.logger.Info("async dispatcher started", "queue", d.queueName)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if n := d.pollOnce(ctx); n == 0 {
			if !sleepCtx(ctx, d.pollInterval) {
				return ctx.Err()
			}
		}
	}
}

// pollOnce leases one batch and delivers it concurrently, returning the count.
func (d *Dispatcher) pollOnce(ctx context.Context) int {
	msgs, err := d.q.Lease(ctx, d.queueName, d.batchSize, d.leaseDuration)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.Error(err, "lease failed", "queue", d.queueName)
		}
		return 0
	}
	var wg sync.WaitGroup
	for _, msg := range msgs {
		wg.Go(func() { d.process(ctx, msg) })
	}
	wg.Wait()
	return len(msgs)
}

// process delivers one leased invocation and settles it per the settle matrix.
func (d *Dispatcher) process(ctx context.Context, msg statestore.LeasedMessage) {
	env, err := Decode(msg.Body)
	if err != nil {
		d.kill(ctx, msg, ReasonUndecodable)
		return
	}
	// Dead-letter an invocation that waited past its MaxAge before delivering it.
	if d.expired(env, d.now()) {
		d.kill(ctx, msg, ReasonExpired)
		return
	}

	dctx, cancel := context.WithTimeout(ctx, d.deliveryTimeout(env))
	res := d.deliverer.Deliver(dctx, env, msg.ID, msg.Attempts)
	cancel()
	recordDelivery(ctx, deliveryCondition(res))

	switch classify(res) {
	case actionAck:
		if err := d.q.Ack(ctx, msg.Receipt); err != nil {
			d.logger.Error(err, "ack failed", "id", msg.ID)
		}
	case actionKill:
		d.kill(ctx, msg, ReasonHTTP4xx)
	case actionRetry:
		d.retry(ctx, msg, env)
	}
}

// retry either requeues with backoff or dead-letters when the attempt budget is
// spent or the next attempt would exceed MaxAge.
func (d *Dispatcher) retry(ctx context.Context, msg statestore.LeasedMessage, env Envelope) {
	if msg.Attempts >= maxAttempts(env.Policy) {
		d.kill(ctx, msg, statestore.ReasonRetriesExhausted)
		return
	}
	backoff := d.backoff(env.Policy, msg.Attempts)
	// If the retry would land after MaxAge, dead-letter now rather than requeue
	// work that can only expire (invariant A4: the reason is the true one).
	if d.now().Add(backoff).Sub(env.EnqueueTime) > maxAge(env.Policy) {
		d.kill(ctx, msg, ReasonExpired)
		return
	}
	if err := d.q.Nack(ctx, msg.Receipt, backoff); err != nil {
		d.logger.Error(err, "nack failed", "id", msg.ID)
		return
	}
	recordRetry(ctx)
}

// kill dead-letters the message and, only on a successful settle, records the DLQ
// counter — a stale-receipt Kill (a newer lease already settled) must not be
// counted as a dead-letter this delivery caused.
func (d *Dispatcher) kill(ctx context.Context, msg statestore.LeasedMessage, reason string) {
	if err := d.q.Kill(ctx, msg.Receipt, reason); err != nil {
		d.logger.Error(err, "kill failed", "id", msg.ID, "reason", reason)
		return
	}
	recordDLQ(ctx, reason)
}

// deliveryTimeout bounds one delivery attempt: the function timeout plus a buffer,
// but always at least deliveryTimeoutBuffer below the lease so the delivery
// context expires before the lease (invariant A7).
func (d *Dispatcher) deliveryTimeout(env Envelope) time.Duration {
	ft := time.Duration(env.FunctionTimeout) * time.Second
	if ft <= 0 {
		ft = DefaultFunctionTimeout
	}
	timeout := ft + deliveryTimeoutBuffer
	if maxTimeout := d.leaseDuration - deliveryTimeoutBuffer; maxTimeout > 0 && timeout > maxTimeout {
		timeout = maxTimeout
	}
	return timeout
}

// backoff is the delay before the next retry: exponential base·2^(attempt-1)
// capped, with full jitter (a uniform draw in [0, computed)) unless disabled. The
// result is always in [0, cap].
func (d *Dispatcher) backoff(p Policy, attempt int) time.Duration {
	base := p.BackoffBase
	if base <= 0 {
		base = DefaultBackoffBase
	}
	capD := p.BackoffCap
	if capD <= 0 {
		capD = DefaultBackoffCap
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := capD
	if shift := attempt - 1; shift < 62 {
		if e := base << shift; e > 0 && e < capD {
			delay = e
		}
	}
	if !p.NoJitter {
		delay = time.Duration(d.rand() * float64(delay))
	}
	return delay
}

func (d *Dispatcher) expired(env Envelope, now time.Time) bool {
	return now.Sub(env.EnqueueTime) > maxAge(env.Policy)
}

func maxAttempts(p Policy) int {
	if p.MaxAttempts > 0 {
		return p.MaxAttempts
	}
	return DefaultMaxAttempts
}

func maxAge(p Policy) time.Duration {
	if p.MaxAge > 0 {
		return p.MaxAge
	}
	return DefaultMaxAge
}

// sleepCtx sleeps for d or until ctx is cancelled; it returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
