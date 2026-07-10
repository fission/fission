// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// triggerWithPrefix builds an HTTPTrigger with the given prefix/keepPrefix,
// matching the fields rewriteFunctionURL consults.
func triggerWithPrefix(prefix string, keepPrefix bool) *fv1.HTTPTrigger {
	return &fv1.HTTPTrigger{
		Spec: fv1.HTTPTriggerSpec{
			Prefix:     &prefix,
			KeepPrefix: keepPrefix,
		},
	}
}

// TestRewriteFunctionURL locks the URL-rewrite semantics of the proxy path:
// prefix trimming (HTTPTrigger.Spec.Prefix with/without KeepPrefix), the
// default /fission-function/<ns>/<name> stripping with default-namespace
// folding, leading-slash normalization, and scheme/host rewriting to the
// resolved service URL. These are golden tests written before the logic was
// extracted out of RetryingRoundTripper.RoundTrip — do not change expectations
// without an explicit behavior-change decision.
func TestRewriteFunctionURL(t *testing.T) {
	t.Parallel()
	svcURL, err := url.Parse("http://10.1.2.3:8888")
	require.NoError(t, err)

	tests := []struct {
		name     string
		trigger  *fv1.HTTPTrigger
		fnMeta   metav1.ObjectMeta
		reqURL   string
		wantPath string
	}{
		{
			name:     "trigger prefix trimmed",
			trigger:  triggerWithPrefix("/api", false),
			fnMeta:   metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			reqURL:   "http://router.example/api/users?x=1",
			wantPath: "/users",
		},
		{
			name:     "trigger prefix kept",
			trigger:  triggerWithPrefix("/api", true),
			fnMeta:   metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			reqURL:   "http://router.example/api/users",
			wantPath: "/api/users",
		},
		{
			name:     "trigger prefix equals full path normalizes to root",
			trigger:  triggerWithPrefix("/api", false),
			fnMeta:   metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			reqURL:   "http://router.example/api",
			wantPath: "/",
		},
		{
			name:     "default namespace function url subpath",
			trigger:  nil,
			fnMeta:   metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			reqURL:   "http://router.example/fission-function/foo/sub",
			wantPath: "/sub",
		},
		{
			name:     "default namespace function url exact",
			trigger:  nil,
			fnMeta:   metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			reqURL:   "http://router.example/fission-function/foo",
			wantPath: "/",
		},
		{
			name:     "non-default namespace function url",
			trigger:  nil,
			fnMeta:   metav1.ObjectMeta{Name: "foo", Namespace: "ns1"},
			reqURL:   "http://router.example/fission-function/ns1/foo/x",
			wantPath: "/x",
		},
		{
			name:     "relative url without prefix rewrites to root",
			trigger:  nil,
			fnMeta:   metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			reqURL:   "http://router.example/myroute",
			wantPath: "/",
		},
		{
			name:     "empty trigger prefix falls back to function url",
			trigger:  triggerWithPrefix("", false),
			fnMeta:   metav1.ObjectMeta{Name: "foo", Namespace: "default"},
			reqURL:   "http://router.example/fission-function/foo/sub",
			wantPath: "/sub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", tt.reqURL, nil)
			wantQuery := req.URL.RawQuery

			rewriteFunctionURL(logr.Discard(), req, tt.trigger, &tt.fnMeta, svcURL)

			assert.Equal(t, tt.wantPath, req.URL.Path)
			assert.Equal(t, "http", req.URL.Scheme)
			assert.Equal(t, "10.1.2.3:8888", req.URL.Host)
			assert.Equal(t, "10.1.2.3:8888", req.Host)
			assert.Equal(t, wantQuery, req.URL.RawQuery, "query string must be left intact")
		})
	}
}

// TestAddForwardedHostHeader locks the Forwarded / X-Forwarded-Host semantics:
// pre-set headers from an external proxy are left intact; otherwise both
// headers are derived from the request host.
func TestAddForwardedHostHeader(t *testing.T) {
	t.Parallel()

	t.Run("existing Forwarded header left intact", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "http://router.example/x", nil)
		req.Header.Set(FORWARDED, "host=upstream.example;")
		addForwardedHostHeader(req)
		assert.Equal(t, "host=upstream.example;", req.Header.Get(FORWARDED))
		assert.Empty(t, req.Header.Get(X_FORWARDED_HOST))
	})

	t.Run("existing X-Forwarded-Host header left intact", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "http://router.example/x", nil)
		req.Header.Set(X_FORWARDED_HOST, "upstream.example")
		addForwardedHostHeader(req)
		assert.Empty(t, req.Header.Get(FORWARDED))
		assert.Equal(t, "upstream.example", req.Header.Get(X_FORWARDED_HOST))
	})

	// Host-form cases: same body, varying host and expected quoting.
	cases := []struct {
		name, host, wantForwarded string
	}{
		{"fqdn host", "example.com:8888", "host=example.com:8888;"},
		{"ipv4 host", "10.0.0.1:8888", "host=10.0.0.1:8888;"},
		{"fqdn host without port", "example.com", "host=example.com;"},
		// RFC 7239: an IPv6 node identifier contains colons and must be
		// quoted. (The pre-rewrite implementation never quoted: its hostname
		// extraction went through url.Parse of a "HTTP/1.1://host" pseudo-URL,
		// which always yielded an empty hostname, so IPv6 took the FQDN
		// branch.)
		{"ipv6 host with port is quoted", "[2001:db8::1]:8888", `host="[2001:db8::1]:8888";`},
		{"bare ipv6 host is quoted", "2001:db8::1", `host="2001:db8::1";`},
		{"bracketed port-less ipv6 host is quoted", "[2001:db8::1]", `host="[2001:db8::1]";`},
		{"ipv4-mapped ipv6 host is quoted", "[::ffff:10.0.0.1]:8888", `host="[::ffff:10.0.0.1]:8888";`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", "http://router.example/x", nil)
			req.Host = tt.host
			addForwardedHostHeader(req)
			assert.Equal(t, tt.wantForwarded, req.Header.Get(FORWARDED))
			assert.Equal(t, tt.host, req.Header.Get(X_FORWARDED_HOST))
		})
	}
}
