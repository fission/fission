/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package hmac

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignerSetsHeaders(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := r.Header.Get(HeaderTimestamp)
		sig := r.Header.Get(HeaderSignature)
		require.NotEmpty(t, ts)
		require.NotEmpty(t, sig)

		body, _ := io.ReadAll(r.Body)
		tsNum, err := strconv.ParseInt(ts, 10, 64)
		require.NoError(t, err)
		assert.True(t, Verify(secret, r.Method, r.URL.Path, body, tsNum, sig))
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := &http.Client{Transport: NewSigner(secret, http.DefaultTransport, time.Now)}
	resp, err := c.Post(srv.URL+"/v1/archive", "application/octet-stream", strings.NewReader("payload"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, 200, resp.StatusCode)
}

func TestSignerHandlesNilBody(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := r.Header.Get(HeaderTimestamp)
		sig := r.Header.Get(HeaderSignature)
		require.NotEmpty(t, ts)
		require.NotEmpty(t, sig)

		body, _ := io.ReadAll(r.Body)
		tsNum, err := strconv.ParseInt(ts, 10, 64)
		require.NoError(t, err)
		assert.True(t, Verify(secret, r.Method, r.URL.Path, body, tsNum, sig))
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := &http.Client{Transport: NewSigner(secret, http.DefaultTransport, time.Now)}
	resp, err := c.Get(srv.URL + "/v1/archive")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, 200, resp.StatusCode)
}
