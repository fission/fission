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
	config "github.com/fission/fission/pkg/featureconfig"
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

// TestDLQRoutesRegistered asserts the four DLQ paths are registered in the built
// public mux with the right methods (via the shared helper, so both mux builders
// register them).
func TestDLQRoutesRegistered(t *testing.T) {
	t.Parallel()
	ts := newShapeTS(t, []fv1.Function{shapeFn("fn")}, nil)
	public, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	assert.True(t, muxMatches(public, http.MethodGet, dlqPathList), "GET list registered")
	assert.True(t, muxMatches(public, http.MethodGet, dlqPathShow), "GET show registered")
	assert.True(t, muxMatches(public, http.MethodPost, dlqPathRedrive), "POST redrive registered")
	assert.True(t, muxMatches(public, http.MethodPost, dlqPathPurge), "POST purge registered")
}

// TestDLQRoutesAuthGated proves the DLQ paths are NOT in authMiddleware's exemption
// list: with auth enabled a tokenless DLQ request is rejected 401, while the
// exempt /router-healthz probe passes through.
func TestDLQRoutesAuthGated(t *testing.T) {
	t.Setenv("JWT_SIGNING_KEY", "test-key")
	featureConfig := &config.FeatureConfig{}
	featureConfig.AuthConfig.AuthUriPath = "/auth/login"
	gated := authMiddleware(featureConfig)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, p := range []string{dlqPathList, dlqPathShow, dlqPathRedrive, dlqPathPurge} {
		rr := httptest.NewRecorder()
		gated.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		assert.Equalf(t, http.StatusUnauthorized, rr.Code, "%s must require a JWT", p)
	}
	// The exempt probe still passes without a token.
	rr := httptest.NewRecorder()
	gated.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/router-healthz", nil))
	assert.Equal(t, http.StatusOK, rr.Code, "/router-healthz stays exempt")
}
