// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package statestore implements the RFC-0027 built-in MessageQueue provider:
// topics are EventLog streams on the RFC-0021 statestore (topic/<ns>/<topic>,
// written by mqpub), so MessageQueueTriggers work with zero external brokers.
//
// The provider mirrors the kafka provider's shape — one classic mqt head per MQ
// type, leader-only subscriptions, HMAC-signed delivery to the router internal
// listener, MaxRetries → ErrorTopic, ResponseTopic on success — but consumes a
// per-trigger durable cursor instead of a consumer group. The subscription
// protocol (cursor CAS, poison isolation, min-cursor retention) is the
// TLC-checked docs/rfc/specs/eventlogsub.tla model.
package statestore

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/mqtrigger/factory"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/mqtrigger/validator"
	"github.com/fission/fission/pkg/statestore"

	// Register the statestore drivers this head opens: the HTTP client
	// (embedded mode → svc/statestore) and Postgres (external mode → the DB
	// directly). STATESTORE_DRIVER selects at runtime, exactly as in the router.
	_ "github.com/fission/fission/pkg/statestore/client"
	_ "github.com/fission/fission/pkg/statestore/postgres"
)

func init() {
	factory.Register(fv1.MessageQueueTypeStatestore, &Factory{})
	// Topic names must be stream-safe (no "/" — the namespaced stream mapping's
	// injectivity); reuse the CRD-side rule so the trigger and destination
	// grammars cannot drift.
	validator.Register(fv1.MessageQueueTypeStatestore, func(topic string) bool {
		return fv1.ValidateTopicName("topic", topic) == nil
	})
}

// Subscription-loop tuning. Modest constants for the classic head; the KEDA
// lag-scaler phase revisits throughput.
const (
	// defaultPollInterval paces Read polls on an idle topic;
	// MessageQueueTriggerSpec.PollingInterval (seconds) overrides per trigger.
	defaultPollInterval = time.Second
	// readBatch bounds one Read — also the redelivery tail bound on a crash
	// between delivery and cursor commit (at-least-once).
	readBatch = 32
	// retryBackoff spaces delivery retries. A deliberate (small) deviation from
	// the kafka provider's immediate retries: hammering a failing function
	// serves nobody.
	retryBackoff = 500 * time.Millisecond
)

// Factory builds the provider; registered for fv1.MessageQueueTypeStatestore.
type Factory struct{}

// Create implements factory.MessageQueueFactory.
func (f *Factory) Create(logger logr.Logger, mqCfg messageQueue.Config, routerURL string) (messageQueue.MessageQueue, error) {
	return New(logger, mqCfg, routerURL)
}

// Statestore is the provider: it owns the store handles, the signed HTTP client
// for router-internal delivery, and the live-subscription set the retention
// reaper trims from.
type Statestore struct {
	logger    logr.Logger
	routerURL string
	caps      statestore.Capabilities
	el        statestore.EventLog
	kv        statestore.KVStore
	pub       mqpub.TopicPublisher
	client    *http.Client

	subs *subscriptionSet

	reaperStart sync.Once
	// reaperStop ends the retention loop (tests; the head otherwise runs it for
	// the process lifetime).
	reaperStop chan struct{}
	// reaperMaxAge/reaperMaxEvents default from the retention consts; fields so
	// tests can exercise the backstops.
	reaperMaxAge    time.Duration
	reaperMaxEvents int64
	// pollOverride, when positive, replaces every subscription's poll interval
	// (tests only — set before any Subscribe).
	pollOverride time.Duration
}

// New opens the statestore exactly like the router's async path does — the
// driver and DSN come from STATESTORE_DRIVER / STATESTORE_DSN (the chart wires
// embedded → "client" → svc/statestore, external → "postgres" → the secret DSN).
// Open does not dial, so an unreachable store does not block startup; the
// subscription loops surface read errors and retry.
func New(logger logr.Logger, _ messageQueue.Config, routerURL string) (messageQueue.MessageQueue, error) {
	if routerURL == "" {
		return nil, fmt.Errorf("statestore mq provider: router URL is required")
	}
	driver := os.Getenv("STATESTORE_DRIVER")
	if driver == "" {
		return nil, fmt.Errorf("statestore mq provider: STATESTORE_DRIVER is required")
	}
	opened, err := statestore.Open(context.Background(), statestore.Config{Driver: driver, DSN: os.Getenv("STATESTORE_DSN")})
	if err != nil {
		return nil, fmt.Errorf("statestore mq provider: opening statestore: %w", err)
	}
	// NewScoped adds the op metrics (and, in external mode, the conservation
	// reporter) — the same wrapping the router applies.
	caps := statestore.NewScoped(opened, nil)
	el, err := caps.EventLog()
	if err != nil {
		return nil, fmt.Errorf("statestore mq provider: eventlog capability: %w", err)
	}
	kv, err := caps.KV()
	if err != nil {
		return nil, fmt.Errorf("statestore mq provider: kv capability: %w", err)
	}
	s := &Statestore{
		logger:     logger.WithName("statestore_mq"),
		routerURL:  routerURL,
		caps:       caps,
		el:         el,
		kv:         kv,
		pub:        mqpub.NewStatestorePublisher(el),
		client:     newSignedHTTPClient(),
		subs:       newSubscriptionSet(),
		reaperStop: make(chan struct{}),

		reaperMaxAge:    maxStreamAge,
		reaperMaxEvents: maxStreamEvents,
	}
	return s, nil
}

// newSignedHTTPClient mirrors the kafka provider's delivery client:
// /fission-function/<ns>/<name> lives only on the router's internal listener
// (GHSA-3g33-6vg6-27m8), so requests are HMAC-signed with the
// ServiceRouterInternal key when FISSION_INTERNAL_AUTH_SECRET is set (empty =
// the verifier's pass-through mode). The pooled transport outlives individual
// subscriptions.
func newSignedHTTPClient() *http.Client {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
	}
	var rt http.RoundTripper = transport
	if master := os.Getenv("FISSION_INTERNAL_AUTH_SECRET"); master != "" {
		rt = hmacauth.ServiceSigner([]byte(master), hmacauth.ServiceRouterInternal, rt, time.Now)
	}
	return &http.Client{Transport: rt}
}

// Subscribe implements messageQueue.MessageQueue: it starts one durable-cursor
// consumer loop for the trigger. Subscriptions run only on the elected leader
// (the mqt manager binds them to the leader-scoped context), so cursor CAS
// conflicts are confined to leadership transitions — which at-least-once
// delivery already tolerates (eventlogsub.tla).
func (s *Statestore) Subscribe(ctx context.Context, trigger *fv1.MessageQueueTrigger) (messageQueue.Subscription, error) {
	if trigger.Spec.FunctionReference.Type != fv1.FunctionReferenceTypeFunctionName {
		return nil, fmt.Errorf("statestore mq provider: unsupported function reference type %q for trigger %s",
			trigger.Spec.FunctionReference.Type, trigger.Name)
	}
	if err := fv1.ValidateTopicName("spec.topic", trigger.Spec.Topic); err != nil {
		return nil, fmt.Errorf("statestore mq provider: %w", err)
	}
	sub := newSubscription(s, trigger)
	s.subs.add(sub)
	subCtx, cancel := context.WithCancel(ctx)
	sub.cancel = cancel
	go func() {
		defer close(sub.done)
		defer s.subs.remove(sub)
		sub.run(subCtx)
	}()
	s.reaperOnce(ctx)
	return sub, nil
}

// Unsubscribe implements messageQueue.MessageQueue.
func (s *Statestore) Unsubscribe(sub messageQueue.Subscription) error {
	return sub.Stop()
}
