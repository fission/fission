// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package client is the HTTP client driver for the embedded statestore: it
// implements the three capability interfaces against the embedded store service
// (pkg/statestore/httpapi), so consumers are byte-identical whether Postgres,
// SQLite, or the embedded store is behind them.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/httpapi"
	"github.com/fission/fission/pkg/utils/httpx"
)

func init() {
	statestore.Register("client", func(_ context.Context, c statestore.Config) (statestore.Capabilities, error) {
		if c.DSN == "" {
			return nil, fmt.Errorf("statestore/client: empty DSN (embedded store base URL)")
		}
		return New(c.DSN, signedClient()), nil
	})
}

// signedClient returns an http.Client that signs requests with the
// ServiceStatestore HMAC key when FISSION_INTERNAL_AUTH_SECRET is set, which the
// embedded statestore head verifies for that identity (statestoresvc). This
// mirrors the timer/mqtrigger/router-async publishers; an empty secret leaves
// requests unsigned (pass-through), matching the server's pass-through mode. Read
// here at the driver-open boundary (not in New) so the deterministic New(dsn, hc)
// constructor stays env-free for tests.
func signedClient() *http.Client {
	// A pooled transport, not http.DefaultTransport: its MaxIdleConnsPerHost
	// of 2 forces every statestore call beyond two-way concurrency onto a
	// fresh connection, piling client-side TIME_WAIT until dials fail with
	// EADDRNOTAVAIL under parallel-join bursts.
	base := httpx.PooledTransport(64)
	master := os.Getenv("FISSION_INTERNAL_AUTH_SECRET")
	if master == "" {
		return &http.Client{Transport: base, Timeout: 30 * time.Second} // unsigned (pass-through mode)
	}
	return &http.Client{
		Transport: hmacauth.ServiceSigner([]byte(master), hmacauth.ServiceStatestore, base, time.Now),
		Timeout:   30 * time.Second,
	}
}

// drainClose reads the response to EOF (bounded) before closing so the
// connection returns to the pool — json.Decoder stops at the end of the
// value and the trailing newline alone defeats keep-alive reuse.
func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// Client is an HTTP-backed Capabilities. It implements all three capability
// interfaces; whether the server actually provides a capability surfaces as
// ErrCapabilityUnavailable on the operation.
type Client struct {
	baseURL string
	hc      *http.Client
}

// New returns a client against the embedded store at baseURL. If hc is nil a
// default client is used; callers inject an HMAC-signing http.Client for the
// authenticated internal listener.
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: baseURL, hc: hc}
}

func (c *Client) KV() (statestore.KVStore, error)        { return c, nil }
func (c *Client) EventLog() (statestore.EventLog, error) { return c, nil }
func (c *Client) Queue() (statestore.Queue, error)       { return c, nil }
func (c *Client) Close() error                           { c.hc.CloseIdleConnections(); return nil }

func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+httpapi.PathReadyz, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	if resp.StatusCode/100 != 2 {
		return decodeErr(resp)
	}
	return nil
}

// postJSON sends req as JSON to path and decodes a 2xx body into out. A non-2xx
// status is mapped back to its statestore sentinel.
func postJSON[Req, Resp any](c *Client, ctx context.Context, path string, req Req, out *Resp) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, path, body)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	return json.NewDecoder(resp.Body).Decode(out)
}

// postNoResponse is postJSON for endpoints whose 2xx reply carries no body.
func postNoResponse[Req any](c *Client, ctx context.Context, path string, req Req) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, path, body)
	if err != nil {
		return err
	}
	drainClose(resp)
	return nil
}

// post sends a JSON body to path and returns the 2xx response, its body still
// open; a non-2xx status is drained and mapped back to its statestore sentinel.
func (c *Client) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer drainClose(resp)
		return nil, decodeErr(resp)
	}
	return resp, nil
}

func decodeErr(resp *http.Response) error {
	var e httpapi.Error
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Code == "" {
		return fmt.Errorf("statestore/client: unexpected status %d", resp.StatusCode)
	}
	if e.Code == httpapi.CodeVersionConflict {
		// Carry the head so Append can resynchronize; it still Is-matches
		// ErrVersionConflict for every other caller.
		return &conflictError{head: e.Head}
	}
	return httpapi.CodeToErr(e.Code, e.Message)
}

// conflictError is an ErrVersionConflict that carries the stream head the
// server reported, so EventLog.Append can return it (the contract makes the
// head meaningful on a conflict). It Is-matches the sentinel so callers that
// only test errors.Is(err, ErrVersionConflict) are unaffected.
type conflictError struct{ head int64 }

func (e *conflictError) Error() string { return statestore.ErrVersionConflict.Error() }
func (e *conflictError) Is(target error) bool {
	return target == statestore.ErrVersionConflict
}

// --- KVStore ---

func (c *Client) Get(ctx context.Context, s statestore.Scope, key string) (statestore.Value, error) {
	var resp httpapi.KVGetResp
	if err := postJSON(c, ctx, httpapi.PathKVGet, httpapi.KVGetReq{Scope: s, Key: key}, &resp); err != nil {
		return statestore.Value{}, err
	}
	return statestore.Value{Data: resp.Value, Version: resp.Version}, nil
}

func (c *Client) Set(ctx context.Context, s statestore.Scope, key string, val []byte, o statestore.SetOptions) error {
	return postNoResponse(c, ctx, httpapi.PathKVSet, httpapi.KVSetReq{
		Scope: s, Key: key, Value: val, IfVersion: o.IfVersion, TTLNanos: o.TTL.Nanoseconds(),
	})
}

// SetCounted implements statestore.CountedKV by forwarding the budget to the
// server, whose backing driver performs the atomic counted set.
func (c *Client) SetCounted(ctx context.Context, s statestore.Scope, key string, val []byte, o statestore.SetOptions, maxKeys int64) error {
	return postNoResponse(c, ctx, httpapi.PathKVSet, httpapi.KVSetReq{
		Scope: s, Key: key, Value: val, IfVersion: o.IfVersion, TTLNanos: o.TTL.Nanoseconds(), MaxKeys: maxKeys,
	})
}

func (c *Client) Delete(ctx context.Context, s statestore.Scope, key string, ifVersion int64) error {
	return postNoResponse(c, ctx, httpapi.PathKVDelete, httpapi.KVDeleteReq{Scope: s, Key: key, IfVersion: ifVersion})
}

func (c *Client) List(ctx context.Context, s statestore.Scope, prefix string, page statestore.Page) (statestore.KeyPage, error) {
	var resp httpapi.KVListResp
	if err := postJSON(c, ctx, httpapi.PathKVList, httpapi.KVListReq{Scope: s, Prefix: prefix, Page: page}, &resp); err != nil {
		return statestore.KeyPage{}, err
	}
	return statestore.KeyPage{Keys: resp.Keys, Next: resp.Next}, nil
}

// --- EventLog ---

func (c *Client) Append(ctx context.Context, stream string, expectedSeq int64, events []statestore.Event) (int64, error) {
	var resp httpapi.EventAppendResp
	if err := postJSON(c, ctx, httpapi.PathEventAppend, httpapi.EventAppendReq{Stream: stream, ExpectedSeq: expectedSeq, Events: events}, &resp); err != nil {
		// A conflict carries the current head so the CAS caller can
		// resynchronize (contract: the head is meaningful on ErrVersionConflict).
		if ce, ok := errors.AsType[*conflictError](err); ok {
			return ce.head, statestore.ErrVersionConflict
		}
		return 0, err
	}
	return resp.Head, nil
}

func (c *Client) Read(ctx context.Context, stream string, fromSeq int64, limit int) ([]statestore.Event, error) {
	var resp httpapi.EventReadResp
	if err := postJSON(c, ctx, httpapi.PathEventRead, httpapi.EventReadReq{Stream: stream, FromSeq: fromSeq, Limit: limit}, &resp); err != nil {
		return nil, err
	}
	return resp.Events, nil
}

func (c *Client) Trim(ctx context.Context, stream string, belowSeq int64) error {
	return postNoResponse(c, ctx, httpapi.PathEventTrim, httpapi.EventTrimReq{Stream: stream, BelowSeq: belowSeq})
}

func (c *Client) Head(ctx context.Context, stream string) (int64, error) {
	var resp httpapi.EventHeadResp
	if err := postJSON(c, ctx, httpapi.PathEventHead, httpapi.EventHeadReq{Stream: stream}, &resp); err != nil {
		return 0, err
	}
	return resp.Head, nil
}

// --- Queue ---

func (c *Client) Enqueue(ctx context.Context, queue string, msg statestore.Message, o statestore.EnqueueOptions) (string, error) {
	var resp httpapi.QueueEnqueueResp
	if err := postJSON(c, ctx, httpapi.PathQueueEnqueue, httpapi.QueueEnqueueReq{
		Queue: queue, Body: msg.Body, DelayNanos: o.Delay.Nanoseconds(), DedupKey: o.DedupKey,
	}, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) Lease(ctx context.Context, queue string, n int, leaseFor time.Duration) ([]statestore.LeasedMessage, error) {
	var resp httpapi.QueueLeaseResp
	if err := postJSON(c, ctx, httpapi.PathQueueLease, httpapi.QueueLeaseReq{Queue: queue, N: n, LeaseForNanos: leaseFor.Nanoseconds()}, &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

func (c *Client) Ack(ctx context.Context, receipt string) error {
	return postNoResponse(c, ctx, httpapi.PathQueueAck, httpapi.QueueAckReq{Receipt: receipt})
}

func (c *Client) Nack(ctx context.Context, receipt string, retryAfter time.Duration) error {
	return postNoResponse(c, ctx, httpapi.PathQueueNack, httpapi.QueueNackReq{Receipt: receipt, RetryAfterNanos: retryAfter.Nanoseconds()})
}

func (c *Client) Kill(ctx context.Context, receipt string, reason string) error {
	return postNoResponse(c, ctx, httpapi.PathQueueKill, httpapi.QueueKillReq{Receipt: receipt, Reason: reason})
}

func (c *Client) DeadLetters(ctx context.Context, queue string, page statestore.Page) ([]statestore.DeadMessage, error) {
	var resp httpapi.QueueDeadLettersResp
	if err := postJSON(c, ctx, httpapi.PathQueueDeadLetter, httpapi.QueueDeadLettersReq{Queue: queue, Page: page}, &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

func (c *Client) Redrive(ctx context.Context, queue string, ids []string) (int64, error) {
	var resp httpapi.QueueRedriveResp
	if err := postJSON(c, ctx, httpapi.PathQueueRedrive, httpapi.QueueRedriveReq{Queue: queue, IDs: ids}, &resp); err != nil {
		return 0, err
	}
	return resp.Redriven, nil
}

func (c *Client) Purge(ctx context.Context, queue string) (int64, error) {
	var resp httpapi.QueuePurgeResp
	if err := postJSON(c, ctx, httpapi.PathQueuePurge, httpapi.QueuePurgeReq{Queue: queue}, &resp); err != nil {
		return 0, err
	}
	return resp.Purged, nil
}

func (c *Client) Stats(ctx context.Context, queue string) (statestore.QueueStats, error) {
	var resp httpapi.QueueStatsResp
	if err := postJSON(c, ctx, httpapi.PathQueueStats, httpapi.QueueStatsReq{Queue: queue}, &resp); err != nil {
		return statestore.QueueStats{}, err
	}
	return statestore.QueueStats{
		Visible:          resp.Visible,
		Leased:           resp.Leased,
		Dead:             resp.Dead,
		OldestVisibleAge: time.Duration(resp.OldestVisibleAgeNanos),
	}, nil
}
