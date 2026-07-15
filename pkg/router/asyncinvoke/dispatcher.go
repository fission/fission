// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"context"
	"errors"
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

	// settleTimeout bounds a terminal settle (Ack/Nack/Kill) on the detached
	// context, so a settle during graceful drain persists the outcome of finished
	// work without hanging shutdown.
	settleTimeout = 15 * time.Second

	// MaxChainDepth bounds the destination chain (invariant A6): a function
	// destination that would enqueue at a depth above it is dropped. Depth 0 is the
	// direct caller, so a self-looping onSuccess stops after MaxChainDepth hops.
	MaxChainDepth = 3
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

// FunctionConfig is a destination function's resolved async config, used by the
// dispatcher to build a fired destination invocation's self-contained envelope.
type FunctionConfig struct {
	Policy          Policy
	OnSuccess       *Destination
	OnFailure       *Destination
	FunctionTimeout int
}

// FunctionConfigResolver resolves a function's async config at destination-fire
// time (a Function-cache read). found is false when the function is absent, so a
// destination pointing at a deleted function is dropped rather than looping.
type FunctionConfigResolver func(ctx context.Context, namespace, name string) (FunctionConfig, bool)

// TopicPublishFunc publishes a settled invocation's result envelope to a
// namespaced topic (RFC-0027). The signature matches mqpub.TopicPublisher.Publish
// — all strings, injected as a function so this package stays a pure library
// (no fv1, no mqpub import). nil → topic destinations are dropped as
// unsupported, the pre-eventing behavior.
type TopicPublishFunc func(ctx context.Context, namespace, mqType, topic, contentType string, payload []byte) error

// MQTypeStatestore mirrors fv1.MessageQueueTypeStatestore without importing the
// CRD package: the RFC-0027 built-in provider, the only topic type the
// dispatcher publishes in phase 1 (admission enforces it; this is the envelope-
// level guard against a forged or legacy record).
const MQTypeStatestore = "statestore"

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

	// ResolveFunctionConfig resolves a destination function's config when firing a
	// function destination. nil → function destinations are dropped (logged).
	ResolveFunctionConfig FunctionConfigResolver

	// PublishTopic publishes a topic destination's result envelope (RFC-0027).
	// nil → topic destinations are dropped as unsupported (logged + metered).
	PublishTopic TopicPublishFunc

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
	resolveFn     FunctionConfigResolver
	publishFn     TopicPublishFunc
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
		resolveFn:     opts.ResolveFunctionConfig,
		publishFn:     opts.PublishTopic,
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
// The terminal settle (Ack/Nack/Kill) runs on a context detached from ctx, so a
// settle for already-completed work still lands during a graceful drain rather
// than being abandoned to a lease-expiry redelivery. Lease and Deliver keep ctx
// and abort on shutdown.
func (d *Dispatcher) process(ctx context.Context, msg statestore.LeasedMessage) {
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer scancel()

	env, err := Decode(msg.Body)
	if err != nil {
		d.logger.Error(err, "async envelope will not decode; dead-lettering", "id", msg.ID)
		d.killReason(sctx, msg, ReasonUndecodable) // no envelope → no destination
		return
	}
	policy := resolvePolicy(env.Policy)

	// Dead-letter an invocation that waited past its MaxAge before delivering it —
	// no delivery happened, so the result envelope carries a zero response.
	if d.now().Sub(env.EnqueueTime) > policy.MaxAge {
		d.settleFail(sctx, msg, env, DeliveryResult{}, ReasonExpired, ConditionEventAgeExceeded)
		return
	}

	dctx, dcancel := context.WithTimeout(ctx, d.deliveryTimeout(env))
	res := d.deliverer.Deliver(dctx, env, msg.ID, msg.Attempts)
	dcancel()
	recordDelivery(sctx, deliveryCondition(res))

	action := classify(res)
	if action != actionAck {
		// One V(1) line with the per-invocation detail (function, status, error)
		// an operator needs to root-cause a delivery failure — the aggregate
		// deliveries/dlq counters alone cannot attribute it.
		d.logger.V(1).Info("async delivery failed",
			"id", msg.ID, "namespace", env.Namespace, "function", env.Function,
			"attempt", msg.Attempts, "statusCode", res.StatusCode, "err", res.Err)
	}

	switch action {
	case actionAck:
		d.settleSuccess(sctx, msg, env, res)
	case actionKill:
		d.settleFail(sctx, msg, env, res, ReasonHTTP4xx, ConditionHTTP4xx)
	case actionRetry:
		d.retry(sctx, msg, env, policy, res)
	}
}

// settleSuccess acks the delivered message and, only when the Ack actually landed
// (not a stale receipt — A3), fires the OnSuccess destination. It mirrors
// settleFail so every settle arm of process is a single settle-then-fire call.
func (d *Dispatcher) settleSuccess(ctx context.Context, msg statestore.LeasedMessage, env Envelope, res DeliveryResult) {
	if err := d.q.Ack(ctx, msg.Receipt); err != nil {
		d.logSettle("ack", msg.ID, err)
		return // a stale/failed ack must not fire the OnSuccess destination (A3)
	}
	d.fireDestination(ctx, env.OnSuccess, env.Depth, d.buildResult(env, msg, ConditionSuccess, res))
}

// retry either requeues with backoff or dead-letters when the attempt budget is
// spent or the next attempt would exceed MaxAge. policy is resolved; res is the
// last delivery result (for the OnFailure result envelope).
func (d *Dispatcher) retry(ctx context.Context, msg statestore.LeasedMessage, env Envelope, policy Policy, res DeliveryResult) {
	if msg.Attempts >= policy.MaxAttempts {
		d.settleFail(ctx, msg, env, res, statestore.ReasonRetriesExhausted, ConditionRetriesExhausted)
		return
	}
	backoff := d.backoff(policy, msg.Attempts)
	// If the retry would land after MaxAge, dead-letter now rather than requeue
	// work that can only expire (invariant A4: the reason is the true one).
	if d.now().Add(backoff).Sub(env.EnqueueTime) > policy.MaxAge {
		d.settleFail(ctx, msg, env, res, ReasonExpired, ConditionEventAgeExceeded)
		return
	}
	if err := d.q.Nack(ctx, msg.Receipt, backoff); err != nil {
		d.logSettle("nack", msg.ID, err)
		return
	}
	recordRetry(ctx)
}

// settleFail dead-letters the message and, only when the Kill actually settled
// (not a stale receipt — A3), fires the OnFailure destination with the reason's
// condition.
func (d *Dispatcher) settleFail(ctx context.Context, msg statestore.LeasedMessage, env Envelope, res DeliveryResult, reason, condition string) {
	if !d.killReason(ctx, msg, reason) {
		return
	}
	d.fireDestination(ctx, env.OnFailure, env.Depth, d.buildResult(env, msg, condition, res))
}

// killReason dead-letters the message and, only on a successful settle, records
// the DLQ counter and returns true — a stale-receipt Kill (a newer lease already
// settled) must not be counted as a dead-letter or trigger a destination.
func (d *Dispatcher) killReason(ctx context.Context, msg statestore.LeasedMessage, reason string) bool {
	if err := d.q.Kill(ctx, msg.Receipt, reason); err != nil {
		d.logSettle("kill", msg.ID, err)
		return false
	}
	recordDLQ(ctx, reason)
	return true
}

// logSettle logs a settle error, quieting the expected stale-receipt case — a
// delivery whose lease a newer lease already superseded (invariant A3) — to V(1)
// so it does not drown a genuine store failure (DB down, receipt bug) logged at
// Error.
func (d *Dispatcher) logSettle(op, id string, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, statestore.ErrInvalidReceipt) {
		d.logger.V(1).Info("settle raced a newer lease (expected)", "op", op, "id", id)
		return
	}
	d.logger.Error(err, op+" failed", "id", id)
}

// fireDestination invokes a settled invocation's destination. It is best-effort —
// the primary is already settled, so a failure here cannot un-settle it — but
// always observable via the destinations metric. depth is the SOURCE invocation's
// depth; a function destination enqueues at depth+1 and is dropped once the chain
// would exceed MaxChainDepth (invariant A6).
func (d *Dispatcher) fireDestination(ctx context.Context, dest *Destination, depth int, result ResultEnvelope) {
	if dest == nil {
		return
	}
	if dest.IsTopic() {
		d.publishTopicDestination(ctx, dest, result)
		return
	}
	next := depth + 1
	// Drop once the chain would exceed MaxChainDepth; the negative/zero guard also
	// rejects a forged envelope with a corrupt (negative or overflowed) depth, so the
	// cap holds regardless of provenance (invariant A6).
	if next <= 0 || next > MaxChainDepth {
		recordDepthCap(ctx)
		recordDestination(ctx, "depth_capped")
		d.logger.Info("async destination chain hit the depth cap; dropping",
			"function", dest.FunctionName, "depth", next, "cap", MaxChainDepth)
		return
	}
	var (
		cfg   FunctionConfig
		found bool
	)
	if d.resolveFn != nil {
		cfg, found = d.resolveFn(ctx, dest.FunctionNamespace, dest.FunctionName)
	}
	if !found {
		// The resolver reports absent OR unresolvable (it logs a real lookup error at
		// Error itself); either way the destination cannot be fired.
		recordDestination(ctx, "dropped")
		d.logger.Info("async destination function unavailable; dropping",
			"namespace", dest.FunctionNamespace, "function", dest.FunctionName)
		return
	}
	body, err := result.Encode()
	if err != nil {
		recordDestination(ctx, "encode_error")
		d.logger.Error(err, "encoding destination result envelope", "function", dest.FunctionName)
		return
	}
	env := Envelope{
		Version:         EnvelopeVersion,
		Namespace:       dest.FunctionNamespace,
		Function:        dest.FunctionName,
		Method:          http.MethodPost,
		Headers:         map[string]string{"Content-Type": "application/json"},
		Body:            body,
		EnqueueTime:     d.now(),
		Depth:           next,
		FunctionTimeout: cfg.FunctionTimeout,
		Policy:          cfg.Policy,
		OnSuccess:       cfg.OnSuccess,
		OnFailure:       cfg.OnFailure,
	}
	if _, err := encodeAndEnqueue(ctx, d.q, d.queueName, env, statestore.EnqueueOptions{}); err != nil {
		// The wrapped error already says encode-vs-enqueue; the function names the hop.
		recordDestination(ctx, "enqueue_error")
		d.logger.Error(err, "enqueuing destination invocation", "function", dest.FunctionName)
		return
	}
	recordDestination(ctx, "enqueued")
}

// publishTopicDestination fires a topic destination: the result envelope is
// published to the namespaced statestore topic (RFC-0027 phase 1). A topic is a
// LEAF — it terminates the chain, so the MaxChainDepth accounting does not apply
// (a function consuming the topic starts a fresh chain at depth 0: it is a new
// invocation, not a continuation). Broker types stay unsupported until the
// egress phase; admission enforces that, so a broker type here is a forged or
// legacy envelope and is dropped, counted, and logged.
func (d *Dispatcher) publishTopicDestination(ctx context.Context, dest *Destination, result ResultEnvelope) {
	if dest.MQType != MQTypeStatestore {
		// Admission enforces the type, so this is a forged or legacy envelope —
		// expected-shaped noise, dropped at Info.
		recordDestination(ctx, "topic_unsupported")
		d.logger.Info("async topic destination unsupported; dropping",
			"namespace", dest.FunctionNamespace, "topic", dest.Topic, "mqType", dest.MQType)
		return
	}
	if d.publishFn == nil {
		// Admission ACCEPTED this destination and the user was promised delivery:
		// a nil publisher here is a wiring defect in the embedder, not a forged
		// envelope — a distinct outcome and an Error, so a dashboard never reads
		// it as expected topic_unsupported noise.
		recordDestination(ctx, "publisher_unconfigured")
		d.logger.Error(nil, "async topic destination dropped: no topic publisher wired (dispatcher misconfiguration)",
			"namespace", dest.FunctionNamespace, "topic", dest.Topic)
		return
	}
	body, err := result.Encode()
	if err != nil {
		recordDestination(ctx, "encode_error")
		d.logger.Error(err, "encoding destination result envelope",
			"namespace", dest.FunctionNamespace, "topic", dest.Topic)
		return
	}
	// The result envelope is JSON by construction, so the delivery Content-Type a
	// consuming trigger replays is application/json.
	if err := d.publishFn(ctx, dest.FunctionNamespace, dest.MQType, dest.Topic, "application/json", body); err != nil {
		recordDestination(ctx, "publish_error")
		d.logger.Error(err, "publishing destination result to topic",
			"namespace", dest.FunctionNamespace, "topic", dest.Topic)
		return
	}
	recordDestination(ctx, "published")
}

// buildResult assembles the Lambda-shaped result envelope for a destination. The
// request payload is included only when the original body fits MaxPayloadBytes
// (RequestPayloadOmitted flags the elision); the response payload was captured and
// truncation-flagged by the deliverer, so a destination can tell partial from whole.
func (d *Dispatcher) buildResult(env Envelope, msg statestore.LeasedMessage, condition string, res DeliveryResult) ResultEnvelope {
	re := ResultEnvelope{
		Version: EnvelopeVersion,
		RequestContext: RequestContext{
			InvocationID: msg.ID,
			FunctionRef:  env.Namespace + "/" + env.Function,
			Condition:    condition,
			Attempts:     msg.Attempts,
			Depth:        env.Depth,
		},
		ResponseContext: ResponseContext{StatusCode: res.StatusCode, Truncated: res.BodyTruncated},
		ResponsePayload: res.Body,
	}
	if len(env.Body) <= MaxPayloadBytes {
		re.RequestPayload = env.Body
	} else {
		re.RequestPayloadOmitted = true
	}
	return re
}

// deliveryTimeout bounds one delivery attempt: the function timeout plus a buffer,
// but always strictly below the lease so the delivery context expires before the
// lease (invariant A7) for ANY lease duration. When the lease is shorter than the
// buffer the floor falls back to half the lease rather than going non-positive
// (which would skip the cap and let a delivery outlive its lease).
func (d *Dispatcher) deliveryTimeout(env Envelope) time.Duration {
	ft := time.Duration(env.FunctionTimeout) * time.Second
	if ft <= 0 {
		ft = DefaultFunctionTimeout
	}
	timeout := ft + deliveryTimeoutBuffer
	maxTimeout := d.leaseDuration - deliveryTimeoutBuffer
	if maxTimeout <= 0 {
		maxTimeout = d.leaseDuration / 2
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	return timeout
}

// backoff is the delay before the next retry: exponential base·2^(attempt-1)
// capped, with full jitter (a uniform draw in [0, computed)) unless disabled. It
// assumes p is a resolved policy (non-zero base/cap); the result is in [0, cap].
func (d *Dispatcher) backoff(p Policy, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := p.BackoffCap
	if shift := attempt - 1; shift < 62 {
		if e := p.BackoffBase << shift; e > 0 && e < p.BackoffCap {
			delay = e
		}
	}
	if !p.NoJitter {
		delay = time.Duration(d.rand() * float64(delay))
	}
	return delay
}

// resolvePolicy fills a Policy's zero fields with the dispatcher's platform
// defaults and clamps MaxAttempts to the store's attempt budget
// (DefaultMaxAttempts). It runs per delivery (not at enqueue) so a default change
// reaches in-flight messages, and it is the single place the "zero means default"
// rule lives — retry/backoff then read plain fields. The MaxAttempts clamp keeps
// the dispatcher's kill-vs-nack decision in step with the store's own exhaustion
// budget: a policy MaxAttempts above the store budget would otherwise let the
// store dead-letter on a Nack the dispatcher believes is a requeue, mis-counting
// a dead-letter as a retry. Admission validation already rejects such values;
// this is the defense-in-depth clamp.
func resolvePolicy(p Policy) Policy {
	if p.MaxAttempts <= 0 || p.MaxAttempts > DefaultMaxAttempts {
		p.MaxAttempts = DefaultMaxAttempts
	}
	if p.BackoffBase <= 0 {
		p.BackoffBase = DefaultBackoffBase
	}
	if p.BackoffCap <= 0 {
		p.BackoffCap = DefaultBackoffCap
	}
	if p.MaxAge <= 0 {
		p.MaxAge = DefaultMaxAge
	}
	return p
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
