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
	"fmt"
	"net/http"
	"time"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/httpapi"
)

func init() {
	statestore.Register("client", func(_ context.Context, c statestore.Config) (statestore.Capabilities, error) {
		if c.DSN == "" {
			return nil, fmt.Errorf("statestore/client: empty DSN (embedded store base URL)")
		}
		return New(c.DSN, nil), nil
	})
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return decodeErr(resp)
	}
	return nil
}

// post sends req as JSON to path and decodes a 2xx body into out (out may be nil
// for endpoints with no response body). A non-2xx status is mapped back to its
// statestore sentinel.
func (c *Client) post(ctx context.Context, path string, req, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return decodeErr(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func decodeErr(resp *http.Response) error {
	var e httpapi.Error
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Code == "" {
		return fmt.Errorf("statestore/client: unexpected status %d", resp.StatusCode)
	}
	return httpapi.CodeToErr(e.Code, e.Message)
}

// --- KVStore ---

func (c *Client) Get(ctx context.Context, s statestore.Scope, key string) (statestore.Value, error) {
	var resp httpapi.KVGetResp
	if err := c.post(ctx, httpapi.PathKVGet, httpapi.KVGetReq{Scope: s, Key: key}, &resp); err != nil {
		return statestore.Value{}, err
	}
	return statestore.Value{Data: resp.Value, Version: resp.Version}, nil
}

func (c *Client) Set(ctx context.Context, s statestore.Scope, key string, val []byte, o statestore.SetOptions) error {
	return c.post(ctx, httpapi.PathKVSet, httpapi.KVSetReq{
		Scope: s, Key: key, Value: val, IfVersion: o.IfVersion, TTLNanos: o.TTL.Nanoseconds(),
	}, nil)
}

func (c *Client) Delete(ctx context.Context, s statestore.Scope, key string, ifVersion int64) error {
	return c.post(ctx, httpapi.PathKVDelete, httpapi.KVDeleteReq{Scope: s, Key: key, IfVersion: ifVersion}, nil)
}

func (c *Client) List(ctx context.Context, s statestore.Scope, prefix string, page statestore.Page) (statestore.KeyPage, error) {
	var resp httpapi.KVListResp
	if err := c.post(ctx, httpapi.PathKVList, httpapi.KVListReq{Scope: s, Prefix: prefix, Page: page}, &resp); err != nil {
		return statestore.KeyPage{}, err
	}
	return statestore.KeyPage{Keys: resp.Keys, Next: resp.Next}, nil
}

// --- EventLog ---

func (c *Client) Append(ctx context.Context, stream string, expectedSeq int64, events []statestore.Event) (int64, error) {
	var resp httpapi.EventAppendResp
	if err := c.post(ctx, httpapi.PathEventAppend, httpapi.EventAppendReq{Stream: stream, ExpectedSeq: expectedSeq, Events: events}, &resp); err != nil {
		return 0, err
	}
	return resp.Head, nil
}

func (c *Client) Read(ctx context.Context, stream string, fromSeq int64, limit int) ([]statestore.Event, error) {
	var resp httpapi.EventReadResp
	if err := c.post(ctx, httpapi.PathEventRead, httpapi.EventReadReq{Stream: stream, FromSeq: fromSeq, Limit: limit}, &resp); err != nil {
		return nil, err
	}
	return resp.Events, nil
}

func (c *Client) Trim(ctx context.Context, stream string, belowSeq int64) error {
	return c.post(ctx, httpapi.PathEventTrim, httpapi.EventTrimReq{Stream: stream, BelowSeq: belowSeq}, nil)
}

// --- Queue ---

func (c *Client) Enqueue(ctx context.Context, queue string, msg statestore.Message, o statestore.EnqueueOptions) (string, error) {
	var resp httpapi.QueueEnqueueResp
	if err := c.post(ctx, httpapi.PathQueueEnqueue, httpapi.QueueEnqueueReq{
		Queue: queue, Body: msg.Body, DelayNanos: o.Delay.Nanoseconds(), DedupKey: o.DedupKey,
	}, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) Lease(ctx context.Context, queue string, n int, leaseFor time.Duration) ([]statestore.LeasedMessage, error) {
	var resp httpapi.QueueLeaseResp
	if err := c.post(ctx, httpapi.PathQueueLease, httpapi.QueueLeaseReq{Queue: queue, N: n, LeaseForNanos: leaseFor.Nanoseconds()}, &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

func (c *Client) Ack(ctx context.Context, receipt string) error {
	return c.post(ctx, httpapi.PathQueueAck, httpapi.QueueAckReq{Receipt: receipt}, nil)
}

func (c *Client) Nack(ctx context.Context, receipt string, retryAfter time.Duration) error {
	return c.post(ctx, httpapi.PathQueueNack, httpapi.QueueNackReq{Receipt: receipt, RetryAfterNanos: retryAfter.Nanoseconds()}, nil)
}

func (c *Client) Kill(ctx context.Context, receipt string, reason string) error {
	return c.post(ctx, httpapi.PathQueueKill, httpapi.QueueKillReq{Receipt: receipt, Reason: reason}, nil)
}

func (c *Client) DeadLetters(ctx context.Context, queue string, page statestore.Page) ([]statestore.DeadMessage, error) {
	var resp httpapi.QueueDeadLettersResp
	if err := c.post(ctx, httpapi.PathQueueDeadLetter, httpapi.QueueDeadLettersReq{Queue: queue, Page: page}, &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

func (c *Client) Redrive(ctx context.Context, queue string, ids []string) error {
	return c.post(ctx, httpapi.PathQueueRedrive, httpapi.QueueRedriveReq{Queue: queue, IDs: ids}, nil)
}
