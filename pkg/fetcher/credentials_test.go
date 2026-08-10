// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestParseArchiveCredentials(t *testing.T) {
	t.Parallel()
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	tests := []struct {
		name    string
		data    map[string][]byte
		want    http.Header
		wantErr string
	}{
		{"basic auth", map[string][]byte{"username": []byte("alice"), "password": []byte("s3cret")},
			http.Header{"Authorization": {basic}}, ""},
		{"bearer token", map[string][]byte{"token": []byte("tok123")},
			http.Header{"Authorization": {"Bearer tok123"}}, ""},
		{"headers block", map[string][]byte{"headers": []byte("X-JFrog-Art-Api: abc\nX-Extra: v1\n")},
			http.Header{"X-Jfrog-Art-Api": {"abc"}, "X-Extra": {"v1"}}, ""},
		{"basic plus extra headers", map[string][]byte{"username": []byte("a"), "password": []byte("b"), "headers": []byte("X-K: v")},
			http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("a:b"))}, "X-K": {"v"}}, ""},
		{"empty secret", map[string][]byte{}, nil, "no recognized credential keys"},
		{"unknown keys only", map[string][]byte{"junk": []byte("x")}, nil, "no recognized credential keys"},
		{"basic and token conflict", map[string][]byte{"username": []byte("a"), "password": []byte("b"), "token": []byte("t")}, nil, "at most one of"},
		{"username without password", map[string][]byte{"username": []byte("a")}, nil, "username and password"},
		{"password without username", map[string][]byte{"password": []byte("b")}, nil, "username and password"},
		{"headers setting authorization with token", map[string][]byte{"token": []byte("t"), "headers": []byte("Authorization: Basic x")}, nil, "Authorization"},
		{"malformed header line", map[string][]byte{"headers": []byte("no-colon-here")}, nil, "invalid header line"},
		{"blank lines and CRLF tolerated", map[string][]byte{"headers": []byte("X-A: 1\r\n\r\nX-B: 2\r\n")},
			http.Header{"X-A": {"1"}, "X-B": {"2"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseArchiveCredentials(tt.data)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// FuzzParseArchiveCredentialHeaders pins that arbitrary header blocks never
// panic and never smuggle an Authorization header past the conflict rule.
func FuzzParseArchiveCredentialHeaders(f *testing.F) {
	f.Add("X-A: 1\nX-B: 2")
	f.Add("Authorization: Bearer x")
	f.Add(":\n::\n")
	f.Fuzz(func(t *testing.T, headers string) {
		h, err := parseArchiveCredentials(map[string][]byte{"token": []byte("t"), "headers": []byte(headers)})
		if err == nil && h.Get("Authorization") != "Bearer t" {
			t.Fatalf("headers block overrode Authorization: %q", h.Get("Authorization"))
		}
	})
}

func TestOriginBoundTransport(t *testing.T) {
	t.Parallel()
	// evil records what a cross-origin redirect target receives.
	var evilGotAuth, evilGotAPIKey bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilGotAuth = r.Header.Get("Authorization") != ""
		evilGotAPIKey = r.Header.Get("X-Api-Key") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()

	// origin serves /direct with creds required, /redirect bounces cross-origin.
	var originSawAuth string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, evil.URL+"/stolen", http.StatusFound)
		default:
			originSawAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer origin.Close()

	creds := http.Header{"Authorization": {"Bearer tok"}, "X-Api-Key": {"k"}}

	client, err := newCredentialClient(origin.URL+"/direct", creds)
	require.NoError(t, err)

	// Same-origin request carries credentials.
	resp, err := client.Get(origin.URL + "/direct")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "Bearer tok", originSawAuth)

	// Cross-origin redirect target receives NO credential headers —
	// including the custom one net/http would forward by itself.
	resp, err = client.Get(origin.URL + "/redirect")
	require.NoError(t, err)
	resp.Body.Close()
	assert.False(t, evilGotAuth, "Authorization leaked cross-origin")
	assert.False(t, evilGotAPIKey, "custom header leaked cross-origin")
}

func TestSameOriginDefaultPorts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		bound, req string
		want       bool
	}{
		{"https://host/p", "https://host:443/q", true},
		{"https://host:443/p", "https://host/q", true},
		{"http://host/p", "http://host:80/q", true},
		{"http://host:8080/p", "http://host/q", false},
		{"https://host/p", "http://host/q", false},       // scheme downgrade
		{"https://host/p", "https://other:443/q", false}, // different host
		{"https://host:8443/p", "https://host:8443/q", true},
	}
	for _, tt := range tests {
		b, err := url.Parse(tt.bound)
		require.NoError(t, err)
		r, err := url.Parse(tt.req)
		require.NoError(t, err)
		assert.Equal(t, tt.want, sameOrigin(b, r), "%s vs %s", tt.bound, tt.req)
	}
}

func TestClientForArchiveUsesSecretRef(t *testing.T) {
	t.Parallel()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("zipbytes"))
	}))
	defer srv.Close()

	kube := k8sfake.NewClientset(&apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "fn-ns"},
		Data:       map[string][]byte{"token": []byte("tok")},
	})
	f := &Fetcher{
		logger:     logr.Discard(),
		kubeClient: kube,
		httpClient: srv.Client(),
		Info:       PodInfo{Name: "p", Namespace: "fn-ns"},
	}

	archive := &fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: srv.URL + "/a.zip",
		SecretRef: &apiv1.LocalObjectReference{Name: "creds"}}
	client, err := f.clientForArchive(t.Context(), archive)
	require.NoError(t, err)
	resp, err := client.Get(srv.URL + "/a.zip")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "Bearer tok", gotAuth)

	// Missing secret is a visible error, not an anonymous fetch.
	archive.SecretRef = &apiv1.LocalObjectReference{Name: "absent"}
	_, err = f.clientForArchive(t.Context(), archive)
	assert.Error(t, err)

	// No SecretRef → the shared unsigned client, untouched behavior.
	archive.SecretRef = nil
	client, err = f.clientForArchive(t.Context(), archive)
	require.NoError(t, err)
	assert.Same(t, f.httpClient, client)

	// Defense in depth: a storagesvc URL with a SecretRef is refused at
	// fetch time even though admission already forbids it.
	archive = &fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "http://storagesvc/v1/archive?id=1",
		SecretRef: &apiv1.LocalObjectReference{Name: "creds"}}
	_, err = f.clientForArchive(t.Context(), archive)
	assert.ErrorContains(t, err, "storage service")
}
