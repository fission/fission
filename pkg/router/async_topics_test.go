// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/memory"
)

// topicTestSet builds an HTTPTriggerSet whose asyncInvoker carries the topic
// admin handles over a fresh in-memory store — the exact MultiPublisher shape
// the router wires, with kafka as the one deployed egress type.
func topicTestSet(t *testing.T) (*HTTPTriggerSet, statestore.Capabilities) {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	q, err := caps.Queue()
	require.NoError(t, err)
	el, err := caps.EventLog()
	require.NoError(t, err)
	pub := mqpub.NewMultiPublisher(mqpub.NewStatestorePublisher(el), mqpub.NewEgressPublisher(q, fv1.MessageQueueTypeKafka))
	ts := &HTTPTriggerSet{logger: logr.Discard(), asyncInvoker: &asyncInvoker{
		queue:    q,
		logger:   logr.Discard(),
		eventLog: el,
		// The router's exact wiring: translate mqpub's sentinel to the
		// dispatcher's at the boundary.
		publishTopic: func(ctx context.Context, ns, mqType, topic, contentType string, payload []byte) error {
			err := pub.Publish(ctx, ns, mqType, topic, contentType, payload)
			if errors.Is(err, mqpub.ErrUnsupportedMQType) {
				return fmt.Errorf("%w: %w", asyncinvoke.ErrTopicUnsupported, err)
			}
			return err
		},
	}}
	return ts, caps
}

func TestTopicPublishAndPeek(t *testing.T) {
	t.Parallel()
	ts, _ := topicTestSet(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, topicPathPublish+"?namespace=ns1&topic=orders", strings.NewReader(`{"n":1}`))
	req.Header.Set("Content-Type", "application/json")
	ts.topicPublish(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = httptest.NewRecorder()
	ts.topicPeek(rr, httptest.NewRequest(http.MethodGet, topicPathPeek+"?namespace=ns1&topic=orders", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var peek topicPeekResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &peek))
	assert.EqualValues(t, 1, peek.Head)
	require.Len(t, peek.Events, 1)
	assert.Equal(t, "application/json", peek.Events[0].Type, "the Content-Type header travels as the event type")
	assert.Equal(t, []byte(`{"n":1}`), peek.Events[0].Payload)
}

func TestTopicPublishStatusMapping(t *testing.T) {
	t.Parallel()
	ts, _ := topicTestSet(t)
	post := func(pathAndQuery, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		ts.topicPublish(rr, httptest.NewRequest(http.MethodPost, pathAndQuery, strings.NewReader(body)))
		return rr
	}

	// Caller mistakes are 400s, never 502 (a typo must not read as a gateway fault).
	assert.Equal(t, http.StatusBadRequest, post(topicPathPublish+"?topic=t", "x").Code, "missing namespace")
	assert.Equal(t, http.StatusBadRequest, post(topicPathPublish+"?namespace=a/b&topic=t", "x").Code, "slash namespace")
	assert.Equal(t, http.StatusBadRequest, post(topicPathPublish+"?namespace=ns&topic=bad/topic", "x").Code, "invalid topic")
	assert.Equal(t, http.StatusBadRequest, post(topicPathPublish+"?namespace=ns&topic=t1&mqtype=nats-jetstream", "x").Code, "unsupported mqtype")
	assert.Equal(t, http.StatusBadRequest, post(topicPathPublish+"?namespace=ns&topic=.&mqtype=kafka", "x").Code, "kafka-invalid topic rejected up front")

	// Oversize body → 413 (the explicit cap, not a generic read failure).
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, topicPathPublish+"?namespace=ns&topic=t1",
		strings.NewReader(strings.Repeat("x", topicPublishMaxBody+1)))
	ts.topicPublish(rr, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

func TestTopicPublishKafkaEnqueuesEgress(t *testing.T) {
	t.Parallel()
	ts, caps := topicTestSet(t)

	rr := httptest.NewRecorder()
	ts.topicPublish(rr, httptest.NewRequest(http.MethodPost, topicPathPublish+"?namespace=ns1&topic=orders&mqtype=kafka", strings.NewReader("ev")))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	q, err := caps.Queue()
	require.NoError(t, err)
	msgs, err := q.Lease(t.Context(), mqpub.EgressQueueForType(fv1.MessageQueueTypeKafka), 10, 60_000_000_000)
	require.NoError(t, err)
	assert.Len(t, msgs, 1, "a kafka publish becomes one durable egress job")
}

func TestTopicPeekBounds(t *testing.T) {
	t.Parallel()
	ts, _ := topicTestSet(t)
	for range 5 {
		rr := httptest.NewRecorder()
		ts.topicPublish(rr, httptest.NewRequest(http.MethodPost, topicPathPublish+"?namespace=ns1&topic=orders", strings.NewReader("e")))
		require.Equal(t, http.StatusOK, rr.Code)
	}

	// limit bounds the tail read; broker mqtypes cannot be peeked.
	rr := httptest.NewRecorder()
	ts.topicPeek(rr, httptest.NewRequest(http.MethodGet, topicPathPeek+"?namespace=ns1&topic=orders&limit=2", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var peek topicPeekResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &peek))
	assert.EqualValues(t, 5, peek.Head)
	require.Len(t, peek.Events, 2)
	assert.EqualValues(t, 4, peek.Events[0].Seq, "the LAST limit events, not the first")

	rr = httptest.NewRecorder()
	ts.topicPeek(rr, httptest.NewRequest(http.MethodGet, topicPathPeek+"?namespace=ns1&topic=orders&mqtype=kafka", nil))
	assert.Equal(t, http.StatusBadRequest, rr.Code, "broker topics cannot be peeked")
}

func TestTopicHandlers501WithoutStatestore(t *testing.T) {
	t.Parallel()
	ts := &HTTPTriggerSet{logger: logr.Discard()}
	for _, h := range []func(http.ResponseWriter, *http.Request){ts.topicPublish, ts.topicPeek} {
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodGet, topicPathPeek+"?namespace=ns&topic=t", nil))
		assert.Equal(t, http.StatusNotImplemented, rr.Code)
	}
}
