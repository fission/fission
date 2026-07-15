// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"io"
	"net/http"
	"strconv"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/utils/httpmux"
)

// RFC-0027 topic admin API — the thin surface behind `fission topic
// publish|peek`. Same posture as the async DLQ API above it: INTERNAL listener
// only (HMAC-verified, NetworkPolicy-gated), 501 when the statestore is not
// wired, and deliberately dev-tool-sized (peek is a bounded tail read, publish
// goes through the exact MultiPublisher async destinations use, so a
// dev-published event is indistinguishable from a destination-published one).
const (
	topicPathPublish = "/v1/eventing/topic/publish"
	topicPathPeek    = "/v1/eventing/topic/peek"

	// topicPeekDefault / topicPeekMax bound a peek; topicPublishMaxBody matches
	// the async body cap (a topic event is the same class of payload).
	topicPeekDefault    = 10
	topicPeekMax        = 100
	topicPublishMaxBody = 256 << 10
)

// topicEvent is one peeked event. Payload is raw bytes (base64 on the JSON
// wire); the CLI renders it as text when printable.
type topicEvent struct {
	Seq     int64     `json:"seq"`
	Type    string    `json:"type,omitempty"`
	Payload []byte    `json:"payload"`
	At      time.Time `json:"at"`
}

type topicPeekResp struct {
	Head   int64        `json:"head"`
	Events []topicEvent `json:"events"`
}

type topicPublishResp struct {
	Published bool `json:"published"`
}

func (ts *HTTPTriggerSet) registerTopicRoutes(internal *httpmux.Mux) {
	internal.HandleFunc(topicPathPublish, ts.topicPublish).Methods(http.MethodPost)
	internal.HandleFunc(topicPathPeek, ts.topicPeek).Methods(http.MethodGet)
}

// topicStore returns the topic surface handles, or writes 501 when the router
// runs without the statestore (async invocation off).
func (ts *HTTPTriggerSet) topicStore(w http.ResponseWriter) bool {
	if ts.asyncInvoker == nil || !ts.asyncInvoker.enabled() || ts.asyncInvoker.eventLog == nil {
		http.Error(w, "eventing is not enabled on this cluster (requires the statestore)", http.StatusNotImplemented)
		return false
	}
	return true
}

// topicParams validates the namespace/topic pair shared by both handlers.
func topicParams(w http.ResponseWriter, r *http.Request) (namespace, topic string, ok bool) {
	namespace = r.URL.Query().Get("namespace")
	topic = r.URL.Query().Get("topic")
	if namespace == "" {
		http.Error(w, "namespace query parameter is required", http.StatusBadRequest)
		return "", "", false
	}
	if err := fv1.ValidateTopicName("topic", topic); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", "", false
	}
	return namespace, topic, true
}

// topicPublish publishes the request body to the topic. ?mqtype selects the
// provider (default statestore); the Content-Type header travels as the event
// type, exactly as a consuming trigger will replay it.
func (ts *HTTPTriggerSet) topicPublish(w http.ResponseWriter, r *http.Request) {
	if !ts.topicStore(w) {
		return
	}
	namespace, topic, ok := topicParams(w, r)
	if !ok {
		return
	}
	mqType := r.URL.Query().Get("mqtype")
	if mqType == "" {
		mqType = fv1.MessageQueueTypeStatestore
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, topicPublishMaxBody))
	if err != nil {
		http.Error(w, "request body exceeds the topic publish limit", http.StatusRequestEntityTooLarge)
		return
	}
	if err := ts.asyncInvoker.publishTopic(r.Context(), namespace, mqType, topic, r.Header.Get("Content-Type"), payload); err != nil {
		ts.logger.Error(err, "topic admin publish", "namespace", namespace, "topic", topic, "mqType", mqType)
		http.Error(w, "publish failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	dlqWriteJSON(w, ts, topicPublishResp{Published: true})
}

// topicPeek returns the last ?limit events of a statestore topic (bounded tail
// read: head, then Read from max(floor, head-limit)). Broker topics cannot be
// peeked — the events live in the broker.
func (ts *HTTPTriggerSet) topicPeek(w http.ResponseWriter, r *http.Request) {
	if !ts.topicStore(w) {
		return
	}
	namespace, topic, ok := topicParams(w, r)
	if !ok {
		return
	}
	limit := topicPeekDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = min(n, topicPeekMax)
		}
	}
	stream := mqpub.StreamForTopic(namespace, topic)
	head, err := ts.asyncInvoker.eventLog.Head(r.Context(), stream)
	if err != nil {
		ts.logger.Error(err, "topic admin peek: reading head", "stream", stream)
		http.Error(w, "reading topic", http.StatusInternalServerError)
		return
	}
	from := max(head-int64(limit), 0)
	events, err := ts.asyncInvoker.eventLog.Read(r.Context(), stream, from, limit)
	if err != nil {
		ts.logger.Error(err, "topic admin peek: reading events", "stream", stream)
		http.Error(w, "reading topic", http.StatusInternalServerError)
		return
	}
	resp := topicPeekResp{Head: head, Events: make([]topicEvent, 0, len(events))}
	for _, ev := range events {
		resp.Events = append(resp.Events, topicEvent{Seq: ev.Seq, Type: ev.Type, Payload: ev.Payload, At: ev.At})
	}
	dlqWriteJSON(w, ts, resp)
}
