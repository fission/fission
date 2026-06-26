// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServiceSigningTransport(t *testing.T) {
	t.Parallel()
	master := []byte("master-key")

	cases := []struct {
		name       string
		master     []byte
		path       string
		wantSigned bool
	}{
		{name: "matching prefix is signed", master: master, path: "/fission-function/foo", wantSigned: true},
		{name: "other path passes through unsigned", master: master, path: "/router-healthz", wantSigned: false},
		{name: "empty master is pass-through", master: nil, path: "/fission-function/foo", wantSigned: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotSig string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotSig = r.Header.Get(HeaderSignature)
			}))
			defer srv.Close()

			rt := NewServiceSigningTransport(tc.master, ServiceRouterInternal, http.DefaultTransport, "/fission-function/")
			req, err := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
			require.NoError(t, err)
			resp, err := rt.RoundTrip(req)
			require.NoError(t, err)
			_ = resp.Body.Close()

			if tc.wantSigned {
				assert.NotEmpty(t, gotSig, "expected a signature header")
			} else {
				assert.Empty(t, gotSig, "expected no signature header")
			}
		})
	}
}
