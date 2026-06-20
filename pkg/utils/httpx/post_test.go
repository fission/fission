// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostWithConnRetry_Success(t *testing.T) {
	var gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	attempts := 0
	err := PostWithConnRetry(t.Context(), srv.Client(), srv.URL, "application/json",
		[]byte(`{"k":"v"}`), logr.Discard(), 3, func() { attempts++ })

	require.NoError(t, err)
	assert.Equal(t, 1, attempts, "a 2xx on the first try should not retry")
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, `{"k":"v"}`, gotBody)
}

func TestPostWithConnRetry_HTTPErrorNoRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := PostWithConnRetry(t.Context(), srv.Client(), srv.URL, "application/json",
		nil, logr.Discard(), 3, func() { attempts++ })

	require.Error(t, err)
	assert.Equal(t, 1, attempts, "an HTTP 5xx is not a connection error and must not be retried")
}

func TestPostWithConnRetry_ConnRefused(t *testing.T) {
	// Bind then immediately close a listener to obtain a definitely-unused port,
	// so the dial is refused rather than hanging.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	attempts := 0
	err := PostWithConnRetry(t.Context(), http.DefaultClient, url, "application/json",
		nil, logr.Discard(), 1, func() { attempts++ })

	require.Error(t, err)
	assert.Equal(t, 1, attempts)
	assert.Contains(t, err.Error(), "post ")
}
