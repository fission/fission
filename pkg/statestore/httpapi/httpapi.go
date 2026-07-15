// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package httpapi is the wire contract for the embedded statestore: the JSON
// request/response types and the sentinel-error mapping shared by the HTTP
// server (pkg/statestore/httpapi server.go) and the client driver
// (pkg/statestore/client). Byte fields ride as base64 (encoding/json's default
// for []byte).
package httpapi

import (
	"errors"

	"github.com/fission/fission/pkg/statestore"
)

// MaxRequestBytes bounds the size of a decoded request body, so the JSON
// decoders never read an unbounded body (this holds even in the HMAC
// pass-through mode where the verifier's own cap is absent). A KV value is capped
// at 256KiB (RFC-0023); base64 + the JSON envelope inflate that, so 4MiB is
// generous headroom.
const MaxRequestBytes = 4 << 20

// Route paths, versioned under /v1.
const (
	PathHealthz         = "/healthz"
	PathReadyz          = "/readyz"
	PathKVGet           = "/v1/kv/get"
	PathKVSet           = "/v1/kv/set"
	PathKVDelete        = "/v1/kv/delete"
	PathKVList          = "/v1/kv/list"
	PathEventAppend     = "/v1/eventlog/append"
	PathEventRead       = "/v1/eventlog/read"
	PathEventTrim       = "/v1/eventlog/trim"
	PathEventHead       = "/v1/eventlog/head"
	PathQueueEnqueue    = "/v1/queue/enqueue"
	PathQueueLease      = "/v1/queue/lease"
	PathQueueAck        = "/v1/queue/ack"
	PathQueueNack       = "/v1/queue/nack"
	PathQueueKill       = "/v1/queue/kill"
	PathQueueDeadLetter = "/v1/queue/deadletters"
	PathQueueRedrive    = "/v1/queue/redrive"
	PathQueuePurge      = "/v1/queue/purge"
	PathQueueStats      = "/v1/queue/stats"
)

// Error is the JSON error envelope. Code is a stable machine string mapped to a
// statestore sentinel on the client.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Stable error codes.
const (
	CodeVersionConflict       = "version_conflict"
	CodeNotFound              = "not_found"
	CodeCapabilityUnavailable = "capability_unavailable"
	CodeQuotaExceeded         = "quota_exceeded"
	CodeInvalidReceipt        = "invalid_receipt"
	CodeClosed                = "closed"
	CodeBadRequest            = "bad_request"
	CodeInternal              = "internal"
)

// codeToErr maps a wire code back to the sentinel it represents (nil if none).
var codeToErr = map[string]error{
	CodeVersionConflict:       statestore.ErrVersionConflict,
	CodeNotFound:              statestore.ErrNotFound,
	CodeCapabilityUnavailable: statestore.ErrCapabilityUnavailable,
	CodeQuotaExceeded:         statestore.ErrQuotaExceeded,
	CodeInvalidReceipt:        statestore.ErrInvalidReceipt,
	CodeClosed:                statestore.ErrClosed,
}

// ErrToCode maps a statestore error to (httpStatus, wireCode).
func ErrToCode(err error) (status int, code string) {
	switch {
	case err == nil:
		return 200, ""
	case errors.Is(err, statestore.ErrVersionConflict):
		return 409, CodeVersionConflict
	case errors.Is(err, statestore.ErrNotFound):
		return 404, CodeNotFound
	case errors.Is(err, statestore.ErrCapabilityUnavailable):
		return 501, CodeCapabilityUnavailable
	case errors.Is(err, statestore.ErrQuotaExceeded):
		return 429, CodeQuotaExceeded
	case errors.Is(err, statestore.ErrInvalidReceipt):
		return 400, CodeInvalidReceipt
	case errors.Is(err, statestore.ErrClosed):
		return 503, CodeClosed
	default:
		return 500, CodeInternal
	}
}

// CodeToErr returns the sentinel for a wire code, or a generic error carrying the
// message when the code is not a known sentinel.
func CodeToErr(code, message string) error {
	if e, ok := codeToErr[code]; ok {
		return e
	}
	if message == "" {
		message = code
	}
	return errors.New("statestore: " + message)
}

// --- KV ---

type KVGetReq struct {
	Scope statestore.Scope `json:"scope"`
	Key   string           `json:"key"`
}
type KVGetResp struct {
	Value   []byte `json:"value"`
	Version int64  `json:"version"`
}
type KVSetReq struct {
	Scope     statestore.Scope `json:"scope"`
	Key       string           `json:"key"`
	Value     []byte           `json:"value"`
	IfVersion *int64           `json:"ifVersion,omitempty"`
	TTLNanos  int64            `json:"ttlNanos,omitempty"`
}
type KVDeleteReq struct {
	Scope     statestore.Scope `json:"scope"`
	Key       string           `json:"key"`
	IfVersion int64            `json:"ifVersion"`
}
type KVListReq struct {
	Scope  statestore.Scope `json:"scope"`
	Prefix string           `json:"prefix"`
	Page   statestore.Page  `json:"page"`
}
type KVListResp struct {
	Keys []string `json:"keys"`
	Next string   `json:"next"`
}

// --- EventLog ---

type EventAppendReq struct {
	Stream      string             `json:"stream"`
	ExpectedSeq int64              `json:"expectedSeq"`
	Events      []statestore.Event `json:"events"`
}
type EventAppendResp struct {
	Head int64 `json:"head"`
}
type EventReadReq struct {
	Stream  string `json:"stream"`
	FromSeq int64  `json:"fromSeq"`
	Limit   int    `json:"limit"`
}
type EventReadResp struct {
	Events []statestore.Event `json:"events"`
}
type EventTrimReq struct {
	Stream   string `json:"stream"`
	BelowSeq int64  `json:"belowSeq"`
}
type EventHeadReq struct {
	Stream string `json:"stream"`
}
type EventHeadResp struct {
	Head int64 `json:"head"`
}

// --- Queue ---

type QueueEnqueueReq struct {
	Queue      string `json:"queue"`
	Body       []byte `json:"body"`
	DelayNanos int64  `json:"delayNanos,omitempty"`
	DedupKey   string `json:"dedupKey,omitempty"`
}
type QueueEnqueueResp struct {
	ID string `json:"id"`
}
type QueueLeaseReq struct {
	Queue         string `json:"queue"`
	N             int    `json:"n"`
	LeaseForNanos int64  `json:"leaseForNanos"`
}
type QueueLeaseResp struct {
	Messages []statestore.LeasedMessage `json:"messages"`
}
type QueueAckReq struct {
	Receipt string `json:"receipt"`
}
type QueueNackReq struct {
	Receipt         string `json:"receipt"`
	RetryAfterNanos int64  `json:"retryAfterNanos"`
}
type QueueKillReq struct {
	Receipt string `json:"receipt"`
	Reason  string `json:"reason"`
}
type QueueDeadLettersReq struct {
	Queue string          `json:"queue"`
	Page  statestore.Page `json:"page"`
}
type QueueDeadLettersResp struct {
	Messages []statestore.DeadMessage `json:"messages"`
}
type QueueRedriveReq struct {
	Queue string   `json:"queue"`
	IDs   []string `json:"ids"`
}
type QueueRedriveResp struct {
	Redriven int64 `json:"redriven"`
}
type QueuePurgeReq struct {
	Queue string `json:"queue"`
}
type QueuePurgeResp struct {
	Purged int64 `json:"purged"`
}
type QueueStatsReq struct {
	Queue string `json:"queue"`
}
type QueueStatsResp struct {
	Visible               int64 `json:"visible"`
	Leased                int64 `json:"leased"`
	Dead                  int64 `json:"dead"`
	OldestVisibleAgeNanos int64 `json:"oldestVisibleAgeNanos"`
}
