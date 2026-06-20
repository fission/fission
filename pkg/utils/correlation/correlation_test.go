// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package correlation

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		inbound  string
		wantSame bool // true => inbound is honored verbatim
	}{
		{name: "honors a plain token", inbound: "req-abc_123.456", wantSame: true},
		{name: "honors a uuid", inbound: uuid.NewString(), wantSame: true},
		{name: "mints when empty", inbound: "", wantSame: false},
		{name: "mints when too long", inbound: strings.Repeat("a", 300), wantSame: false},
		{name: "mints when it carries a control char", inbound: "abc\ndef", wantSame: false},
		{name: "mints when it carries a space", inbound: "abc def", wantSame: false},
		{name: "mints when non-ascii", inbound: "abc€def", wantSame: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ID(tc.inbound)
			require.NotEmpty(t, got)
			if tc.wantSame {
				assert.Equal(t, tc.inbound, got)
				return
			}
			assert.NotEqual(t, tc.inbound, got)
			// A minted id must be a parseable UUID.
			_, err := uuid.Parse(got)
			assert.NoError(t, err, "minted id should be a valid uuid")
		})
	}
}

func TestMiddleware_MintsAndPropagates(t *testing.T) {
	t.Parallel()

	var seenReqHeader, seenCtx string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenReqHeader = r.Header.Get(HeaderRequestID)
		seenCtx = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	Middleware(next).ServeHTTP(rec, req)

	respHeader := rec.Header().Get(HeaderRequestID)
	require.NotEmpty(t, respHeader, "response must carry the request id")
	_, err := uuid.Parse(respHeader)
	require.NoError(t, err, "minted response id should be a valid uuid")

	// The same id must reach the downstream request header and context.
	assert.Equal(t, respHeader, seenReqHeader)
	assert.Equal(t, respHeader, seenCtx)
}

func TestMiddleware_HonorsInboundID(t *testing.T) {
	t.Parallel()

	const inbound = "client-supplied-id-42"
	var seenReqHeader, seenCtx string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenReqHeader = r.Header.Get(HeaderRequestID)
		seenCtx = FromContext(r.Context())
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, inbound)
	Middleware(next).ServeHTTP(rec, req)

	assert.Equal(t, inbound, rec.Header().Get(HeaderRequestID))
	assert.Equal(t, inbound, seenReqHeader)
	assert.Equal(t, inbound, seenCtx)
}

func TestFromContext_EmptyWhenUnset(t *testing.T) {
	t.Parallel()
	assert.Empty(t, FromContext(t.Context()))
}
