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
)

// Error is the JSON body of any non-2xx response.
type Error struct {
	Error string `json:"error"`
	Code  string `json:"code"`
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
