// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/memory"
)

// newBubbleHandler builds the handler without a network listener so it can
// run inside a synctest bubble (virtual clock; the memory driver's time.Now
// is bubble time).
func newBubbleHandler(t *testing.T, fns map[types.NamespacedName]*fv1.StateConfig) http.Handler {
	t.Helper()
	inner, err := memory.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	index := NewFunctionIndex()
	for nn, sc := range fns {
		index.Upsert(nn, sc)
	}
	scoped := statestore.NewScoped(inner, index)
	kv, err := scoped.KV()
	require.NoError(t, err)
	auth := newAuthenticator(testMaster, nil, hmacauth.VerifierOpts{SkewSec: 60})
	return newHandler(kv, index, auth, func() bool { return true }, logr.Discard())
}

func bubbleReq(h http.Handler, method, path, ns, ks, token string, body []byte, hdrs map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set(HeaderStateNamespace, ns)
	req.Header.Set(HeaderStateKeyspace, ks)
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestTTLExpiryVirtualClock pins the TTL write path end to end: an explicit
// header TTL and the keyspace DefaultTTL both expire the key exactly on the
// virtual-clock boundary — no sleeps, no clock seams.
func TestTTLExpiryVirtualClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newBubbleHandler(t, map[types.NamespacedName]*fv1.StateConfig{
			fnA: {DefaultTTL: &metav1.Duration{Duration: 30 * time.Second}},
		})
		tok := stateToken("ns-a", "fn-a")

		// Explicit header TTL wins over the keyspace default.
		rec := bubbleReq(h, http.MethodPut, "/v1/state/short", "ns-a", "fn-a", tok, []byte("v"), map[string]string{HeaderStateTTL: "10s"})
		require.Equal(t, http.StatusNoContent, rec.Code)
		// No header: DefaultTTL (30s) applies.
		rec = bubbleReq(h, http.MethodPut, "/v1/state/dflt", "ns-a", "fn-a", tok, []byte("v"), nil)
		require.Equal(t, http.StatusNoContent, rec.Code)

		time.Sleep(10 * time.Second) // virtual
		assert.Equal(t, http.StatusNotFound, bubbleReq(h, http.MethodGet, "/v1/state/short", "ns-a", "fn-a", tok, nil, nil).Code, "header TTL elapsed")
		assert.Equal(t, http.StatusOK, bubbleReq(h, http.MethodGet, "/v1/state/dflt", "ns-a", "fn-a", tok, nil, nil).Code, "default TTL not yet elapsed")

		time.Sleep(20 * time.Second) // virtual, total 30s
		assert.Equal(t, http.StatusNotFound, bubbleReq(h, http.MethodGet, "/v1/state/dflt", "ns-a", "fn-a", tok, nil, nil).Code, "default TTL elapsed")
	})
}

// TestTokenRotationDualAccept pins the rotation story: while the old master
// is configured (FISSION_INTERNAL_AUTH_SECRET_OLD), tokens derived from it
// still verify; once it is dropped, they stop.
func TestTokenRotationDualAccept(t *testing.T) {
	t.Parallel()
	oldMaster := []byte("previous-master")
	oldToken := stateTokenFor(oldMaster, "ns-a", "fn-a")
	newToken := stateTokenFor(testMaster, "ns-a", "fn-a")

	during := newAuthenticator(testMaster, oldMaster, hmacauth.VerifierOpts{})
	assert.True(t, during.verifyKeyspaceToken(newToken, "ns-a", "fn-a"))
	assert.True(t, during.verifyKeyspaceToken(oldToken, "ns-a", "fn-a"), "dual-accept window")

	after := newAuthenticator(testMaster, nil, hmacauth.VerifierOpts{})
	assert.True(t, after.verifyKeyspaceToken(newToken, "ns-a", "fn-a"))
	assert.False(t, after.verifyKeyspaceToken(oldToken, "ns-a", "fn-a"), "window closed")
}
