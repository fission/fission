// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/memory"
)

// dlqTestSet builds an HTTPTriggerSet whose asyncInvoker is backed by a fresh
// in-memory queue, and dead-letters one invocation per (namespace, function) pair
// so the DLQ handlers have data. It returns the set and the durable ids in order.
func dlqTestSet(t *testing.T, fns ...[2]string) (*HTTPTriggerSet, statestore.Queue, []string) {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	q, err := caps.Queue()
	require.NoError(t, err)

	var ids []string
	for _, nf := range fns {
		body, err := asyncinvoke.Envelope{
			Version: asyncinvoke.EnvelopeVersion, Namespace: nf[0], Function: nf[1],
			Body: []byte("payload-" + nf[1]), EnqueueTime: time.Unix(1_000_000, 0),
		}.Encode()
		require.NoError(t, err)
		id, err := q.Enqueue(t.Context(), asyncinvoke.DefaultQueue, statestore.Message{Body: body}, statestore.EnqueueOptions{})
		require.NoError(t, err)
		l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
		require.NoError(t, q.Kill(t.Context(), l[0].Receipt, "permanent"))
		ids = append(ids, id)
	}
	ts := &HTTPTriggerSet{logger: logr.Discard(), asyncInvoker: &asyncInvoker{queue: q, logger: logr.Discard()}}
	return ts, q, ids
}

func TestDLQList(t *testing.T) {
	t.Parallel()
	ts, _, _ := dlqTestSet(t, [2]string{"ns1", "fn-a"}, [2]string{"ns2", "fn-b"})

	rr := httptest.NewRecorder()
	ts.dlqList(rr, httptest.NewRequest(http.MethodGet, dlqPathList, nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var resp dlqListResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Messages, 2)
	// The envelope's namespace/function are surfaced, and the dead-letter reason.
	byFn := map[string]dlqMessage{}
	for _, m := range resp.Messages {
		byFn[m.Function] = m
	}
	assert.Equal(t, "ns1", byFn["fn-a"].Namespace)
	assert.Equal(t, "permanent", byFn["fn-a"].Reason)
	assert.Equal(t, "ns2", byFn["fn-b"].Namespace)
}

func TestDLQListNamespaceFilter(t *testing.T) {
	t.Parallel()
	ts, _, _ := dlqTestSet(t, [2]string{"ns1", "fn-a"}, [2]string{"ns2", "fn-b"})

	rr := httptest.NewRecorder()
	ts.dlqList(rr, httptest.NewRequest(http.MethodGet, dlqPathList+"?namespace=ns2", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var resp dlqListResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Messages, 1, "namespace filter narrows the list")
	assert.Equal(t, "fn-b", resp.Messages[0].Function)
}

func TestDLQShow(t *testing.T) {
	t.Parallel()
	ts, _, ids := dlqTestSet(t, [2]string{"ns1", "fn-a"})

	rr := httptest.NewRecorder()
	ts.dlqShow(rr, httptest.NewRequest(http.MethodGet, dlqPathShow+"?id="+ids[0], nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var resp dlqShowResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, ids[0], resp.ID)
	require.NotNil(t, resp.Envelope, "show decodes the full envelope")
	assert.Equal(t, "fn-a", resp.Envelope.Function)
	assert.Equal(t, []byte("payload-fn-a"), resp.Envelope.Body)
}

func TestDLQShowNotFound(t *testing.T) {
	t.Parallel()
	ts, _, _ := dlqTestSet(t, [2]string{"ns1", "fn-a"})

	rr := httptest.NewRecorder()
	ts.dlqShow(rr, httptest.NewRequest(http.MethodGet, dlqPathShow+"?id=nope", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code)

	rrNoID := httptest.NewRecorder()
	ts.dlqShow(rrNoID, httptest.NewRequest(http.MethodGet, dlqPathShow, nil))
	assert.Equal(t, http.StatusBadRequest, rrNoID.Code, "id is required")
}

func TestDLQRedrive(t *testing.T) {
	t.Parallel()
	ts, q, ids := dlqTestSet(t, [2]string{"ns1", "fn-a"}, [2]string{"ns2", "fn-b"})

	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"ids":["` + ids[0] + `"]}`)
	ts.dlqRedrive(rr, httptest.NewRequest(http.MethodPost, dlqPathRedrive, body))
	require.Equal(t, http.StatusOK, rr.Code)
	var resp dlqMutateResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.EqualValues(t, 1, resp.Count)

	// The redriven message left the dead set (and is leasable again); the other stays.
	dead, err := q.DeadLetters(t.Context(), asyncinvoke.DefaultQueue, statestore.Page{})
	require.NoError(t, err)
	require.Len(t, dead, 1)
	assert.Equal(t, ids[1], dead[0].ID)
}

func TestDLQRedriveEmptyIDs(t *testing.T) {
	t.Parallel()
	ts, _, _ := dlqTestSet(t, [2]string{"ns1", "fn-a"})
	rr := httptest.NewRecorder()
	ts.dlqRedrive(rr, httptest.NewRequest(http.MethodPost, dlqPathRedrive, strings.NewReader(`{"ids":[]}`)))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestDLQRedriveBodyStatuses pins the decode error mapping: an over-limit body is
// 413 (the explicit dlqMaxBodyBytes bound), malformed JSON is 400.
func TestDLQRedriveBodyStatuses(t *testing.T) {
	t.Parallel()
	ts, _, _ := dlqTestSet(t, [2]string{"ns1", "fn-a"})

	big := `{"ids":["` + strings.Repeat("a", dlqMaxBodyBytes+1) + `"]}`
	rr := httptest.NewRecorder()
	ts.dlqRedrive(rr, httptest.NewRequest(http.MethodPost, dlqPathRedrive, strings.NewReader(big)))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code, "over-limit body → 413")

	rrBad := httptest.NewRecorder()
	ts.dlqRedrive(rrBad, httptest.NewRequest(http.MethodPost, dlqPathRedrive, strings.NewReader(`not json`)))
	assert.Equal(t, http.StatusBadRequest, rrBad.Code, "malformed JSON → 400")
}

func TestDLQPurge(t *testing.T) {
	t.Parallel()
	ts, q, _ := dlqTestSet(t, [2]string{"ns1", "fn-a"}, [2]string{"ns2", "fn-b"})

	rr := httptest.NewRecorder()
	ts.dlqPurge(rr, httptest.NewRequest(http.MethodPost, dlqPathPurge, nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var resp dlqMutateResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.EqualValues(t, 2, resp.Count)

	dead, err := q.DeadLetters(t.Context(), asyncinvoke.DefaultQueue, statestore.Page{})
	require.NoError(t, err)
	assert.Empty(t, dead, "purge emptied the dead set")
}

// TestDLQDisabledReturns501 asserts every DLQ handler fails closed with 501 when
// async invocation is not enabled (nil invoker or nil queue), not a 404/500.
func TestDLQDisabledReturns501(t *testing.T) {
	t.Parallel()
	cases := map[string]*HTTPTriggerSet{
		"nil invoker": {logger: logr.Discard()},
		"nil queue":   {logger: logr.Discard(), asyncInvoker: &asyncInvoker{logger: logr.Discard()}},
	}
	for name, ts := range cases {
		t.Run(name, func(t *testing.T) {
			for _, h := range []http.HandlerFunc{ts.dlqList, ts.dlqShow, ts.dlqRedrive, ts.dlqPurge} {
				rr := httptest.NewRecorder()
				h(rr, httptest.NewRequest(http.MethodGet, "/", strings.NewReader(`{"ids":["x"]}`)))
				assert.Equal(t, http.StatusNotImplemented, rr.Code)
			}
		})
	}
}

// TestDLQRoutesOnInternalListenerNotPublic asserts the four DLQ admin paths are
// registered on the INTERNAL mux (where the listener-level HMAC verifier gates
// them) and are absent from the PUBLIC mux — so an operator running with the
// default authentication.enabled=false does not expose an unauthenticated
// cross-namespace read/redrive/purge surface on the public router.
func TestDLQRoutesOnInternalListenerNotPublic(t *testing.T) {
	t.Parallel()
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")}, nil)
	public, internal, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, dlqPathList},
		{http.MethodGet, dlqPathShow},
		{http.MethodPost, dlqPathRedrive},
		{http.MethodPost, dlqPathPurge},
	} {
		assert.Truef(t, muxMatches(internal, tc.method, tc.path), "%s %s must be on the internal mux", tc.method, tc.path)
		assert.Falsef(t, muxMatches(public, tc.method, tc.path), "%s %s must NOT be on the public mux", tc.method, tc.path)
	}
}

// dlqEgressDeadLetter enqueues one egress job on mq-egress-kafka and dead-letters
// it, returning its durable id.
func dlqEgressDeadLetter(t *testing.T, q statestore.Queue) string {
	t.Helper()
	body, err := json.Marshal(mqpub.EgressJob{Namespace: "ns1", Topic: "orders", ContentType: "text/plain", Payload: []byte("ev-1")})
	require.NoError(t, err)
	queue := mqpub.EgressQueueForType(fv1.MessageQueueTypeKafka)
	id, err := q.Enqueue(t.Context(), queue, statestore.Message{Body: body}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	l, err := q.Lease(t.Context(), queue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	require.NoError(t, q.Kill(t.Context(), l[0].Receipt, "broker down"))
	return id
}

// TestDLQQueueParam: ?queue= selects an egress DLQ (shape-allowlisted), and the
// egress job's identity (namespace/topic) is surfaced in the summary.
func TestDLQQueueParam(t *testing.T) {
	t.Parallel()
	ts, q, _ := dlqTestSet(t, [2]string{"ns1", "fn-a"})
	id := dlqEgressDeadLetter(t, q)

	// The egress queue's dead set is disjoint from the async one.
	rr := httptest.NewRecorder()
	ts.dlqList(rr, httptest.NewRequest(http.MethodGet, dlqPathList+"?queue=mq-egress-kafka", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var resp dlqListResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Messages, 1)
	assert.Equal(t, id, resp.Messages[0].ID)
	assert.Equal(t, "ns1", resp.Messages[0].Namespace)
	assert.Equal(t, "orders", resp.Messages[0].Topic, "egress jobs surface a topic, not a function")
	assert.Empty(t, resp.Messages[0].Function)

	// A malformed queue name is a 400, not an arbitrary-queue read.
	for _, bad := range []string{"mq-egress-", "mq-egress-Kafka", "mq-egress-a/b", "state_queue", "asyncinv2"} {
		rr := httptest.NewRecorder()
		ts.dlqList(rr, httptest.NewRequest(http.MethodGet, dlqPathList+"?queue="+bad, nil))
		assert.Equalf(t, http.StatusBadRequest, rr.Code, "queue %q must be rejected", bad)
	}
}

// TestDLQShowEgressJob: show on an egress dead letter returns the decoded
// EgressJob (payload included), never a half-empty async Envelope fabricated by
// a lenient JSON decode.
func TestDLQShowEgressJob(t *testing.T) {
	t.Parallel()
	ts, q, _ := dlqTestSet(t)
	id := dlqEgressDeadLetter(t, q)

	rr := httptest.NewRecorder()
	ts.dlqShow(rr, httptest.NewRequest(http.MethodGet, dlqPathShow+"?queue=mq-egress-kafka&id="+id, nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var resp dlqShowResp
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Nil(t, resp.Envelope, "an egress job must not decode into a bogus Envelope")
	require.NotNil(t, resp.EgressJob)
	assert.Equal(t, "orders", resp.EgressJob.Topic)
	assert.Equal(t, []byte("ev-1"), resp.EgressJob.Payload, "the failed event's payload is inspectable")
}
