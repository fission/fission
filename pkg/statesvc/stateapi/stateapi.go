// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package stateapi is the RFC-0023 statesvc wire contract: the HTTP headers,
// query parameters, and JSON DTOs shared by the statesvc server
// (pkg/statesvc), the fission CLI (fission fn state), and any function-side
// SDK. It is a dependency-light leaf package (no controller-runtime) so the
// CLI can import it, mirroring pkg/statestore/httpapi for the raw substrate —
// a header or field rename here is a compile-time break in every client
// instead of a silent runtime divergence.
package stateapi

import (
	"regexp"
	"strings"
	"time"
)

// Scope-claim request headers (bearer/function path). The namespace and
// keyspace a request operates on are CLAIMS; they become the store Scope only
// after the per-keyspace bearer token — derived from exactly those claims —
// verifies.
const (
	HeaderNamespace = "X-Fission-State-Namespace"
	HeaderKeyspace  = "X-Fission-State-Keyspace"
	HeaderVersion   = "X-Fission-State-Version"
	HeaderTTL       = "X-Fission-State-TTL"
)

// Admin scope-claim query parameters (CLI/operator HMAC path). These ride the
// query string, not headers, so the HMAC signature (which covers the request
// URI, not headers) binds them — statesvc rejects header-borne admin scope.
const (
	QueryScopeNamespace = "scope-namespace"
	QueryScopeKeyspace  = "scope-keyspace"
)

// Machine-readable error codes returned in Error.Code.
const (
	CodeBadRequest      = "bad_request"
	CodeUnauthorized    = "unauthorized"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeVersionConflict = "version_conflict"
	CodeQuotaValueBytes = "quota_value_bytes"
	CodeQuotaKeys       = "quota_keys"
	CodeUnavailable     = "capability_unavailable"
	CodeInternal        = "internal"
	CodeBadStream       = "bad_stream"
)

// EventLog request/response limits (RFC-0027 §G13): the append batch and read
// page caps, plus the read page default.
const (
	MaxAppendEvents  = 64
	MaxReadLimit     = 500
	DefaultReadLimit = 100
)

// Error is the JSON body of any non-2xx response. Head is set only on a
// version_conflict from POST /v1/eventlog/append, carrying the stream's
// current head so the caller can resynchronize without a separate Head call.
type Error struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Head  *int64 `json:"head,omitempty"`
}

// CASRequest is the POST /v1/state/{key}/cas body — an explicit
// compare-and-swap for clients without If-Match plumbing. Value is base64
// (JSON []byte); ExpectVersion 0 means create-only.
type CASRequest struct {
	ExpectVersion int64  `json:"expectVersion"`
	Value         []byte `json:"value"`
}

// ListResponse is the GET /v1/state body: a page of keys plus the cursor for
// the next page ("" when exhausted).
type ListResponse struct {
	Keys   []string `json:"keys"`
	Cursor string   `json:"cursor,omitempty"`
}

// streamPattern is the caller-visible EventLog stream charset: 1-200 chars of
// [a-zA-Z0-9._/-], with no leading/trailing '/' and no ".." segment (each
// '/'-separated segment excludes '/' itself, so "a//b" and "../x" both fail
// to match).
var streamPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+(/[a-zA-Z0-9._-]+)*$`)

// ValidStream reports whether s is a well-formed caller-visible stream name:
// 1-200 chars of [a-zA-Z0-9._/-], no leading/trailing '/' (streamPattern
// alone enforces both — a leading or trailing '/' cannot start or end a
// segment), and no "." or ".." segment. The dot-segment check is explicit and
// separate from streamPattern: that pattern's character class permits a bare
// "." or ".." within a segment (both are charset-valid), and StreamName later
// concatenates the stream into a '/'-delimited internal path — a ".." segment
// would be a path-traversal escape out of the scope-qualified namespace/keyspace
// prefix, and a "." segment would alias two caller-visible names to one internal
// stream ("a/./b" vs "a/b"), so both are rejected here regardless of charset.
func ValidStream(s string) bool {
	if s == "" || len(s) > 200 || !streamPattern.MatchString(s) {
		return false
	}
	for seg := range strings.SplitSeq(s, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// StreamName maps a caller-visible stream to the internal, scope-qualified
// EventLog stream. It is the single source of the fnstate/ naming shape: the
// EventLog capability has no Scope parameter, so the NAME is the scope —
// every caller (statesvc handlers, and later the agentruntime-side
// consumers) must derive the internal stream through this function, never by
// hand-concatenating the prefix.
func StreamName(ns, keyspace, clientStream string) string {
	return "fnstate/" + ns + "/" + keyspace + "/" + clientStream
}

// EventInput is one event in an EventAppendRequest. Payload is base64 on the
// wire (JSON []byte).
type EventInput struct {
	Type    string `json:"type"`
	Payload []byte `json:"payload"`
}

// EventAppendRequest is the POST /v1/eventlog/append body. ExpectedSeq is a
// required CAS precondition on this API — the wire never exposes
// statestore.AppendAny (expectedSeq < 0 is rejected with 400), so every
// append through statesvc is interleave-safe by construction.
type EventAppendRequest struct {
	Stream      string       `json:"stream"`
	ExpectedSeq int64        `json:"expectedSeq"`
	Events      []EventInput `json:"events"`
}

// EventAppendResponse is the 200 body of a successful append: the stream's
// new head sequence.
type EventAppendResponse struct {
	Head int64 `json:"head"`
}

// EventReadRequest is the POST /v1/eventlog/read body. Limit <= 0 defaults to
// DefaultReadLimit; any value is clamped to MaxReadLimit.
type EventReadRequest struct {
	Stream  string `json:"stream"`
	FromSeq int64  `json:"fromSeq"`
	Limit   int    `json:"limit"`
}

// EventOutput is one event as returned by POST /v1/eventlog/read.
type EventOutput struct {
	Seq     int64     `json:"seq"`
	Type    string    `json:"type"`
	Payload []byte    `json:"payload"`
	At      time.Time `json:"at"`
}

// EventReadResponse is the 200 body of POST /v1/eventlog/read.
type EventReadResponse struct {
	Events []EventOutput `json:"events"`
}

// EventHeadRequest is the POST /v1/eventlog/head body.
type EventHeadRequest struct {
	Stream string `json:"stream"`
}

// EventHeadResponse is the 200 body of POST /v1/eventlog/head.
type EventHeadResponse struct {
	Head int64 `json:"head"`
}
