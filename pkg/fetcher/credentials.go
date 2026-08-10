// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// Credential keys recognized in an Archive.SecretRef Secret (RFC-0031).
// username+password (a kubernetes.io/basic-auth Secret works as-is) or token
// pick the Authorization scheme; headers carries extra headers, one
// "Name: Value" per line, for API-key schemes like X-JFrog-Art-Api.
const (
	credKeyUsername = "username"
	credKeyPassword = "password"
	credKeyToken    = "token"
	credKeyHeaders  = "headers"
)

// parseArchiveCredentials turns a SecretRef Secret's data into the header set
// to attach to same-origin archive requests. Errors are deliberate on every
// ambiguous shape: a named credential that silently does nothing (empty
// Secret, unknown keys, conflicting schemes) is a misconfiguration the user
// must see, not an anonymous fetch that 401s mysteriously.
func parseArchiveCredentials(data map[string][]byte) (http.Header, error) {
	user, hasUser := data[credKeyUsername]
	pass, hasPass := data[credKeyPassword]
	token, hasToken := data[credKeyToken]
	headersBlock, hasHeaders := data[credKeyHeaders]

	if !hasUser && !hasPass && !hasToken && !hasHeaders {
		return nil, fmt.Errorf("credential secret has no recognized credential keys (want %s+%s, %s, or %s)",
			credKeyUsername, credKeyPassword, credKeyToken, credKeyHeaders)
	}
	if hasUser != hasPass {
		return nil, fmt.Errorf("credential secret must set %s and %s together", credKeyUsername, credKeyPassword)
	}
	if hasUser && hasToken {
		return nil, fmt.Errorf("credential secret must set at most one of %s/%s and %s", credKeyUsername, credKeyPassword, credKeyToken)
	}

	h := http.Header{}
	if hasUser {
		// Reuse net/http's canonical basic-auth encoding.
		r, _ := http.NewRequest(http.MethodGet, "http://placeholder.invalid", nil)
		r.SetBasicAuth(string(user), string(pass))
		h.Set("Authorization", r.Header.Get("Authorization"))
	}
	if hasToken {
		h.Set("Authorization", "Bearer "+string(token))
	}
	if hasHeaders {
		for line := range strings.SplitSeq(strings.ReplaceAll(string(headersBlock), "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			name, value, ok := strings.Cut(line, ":")
			name = strings.TrimSpace(name)
			if !ok || name == "" {
				return nil, fmt.Errorf("invalid header line in credential secret (want \"Name: Value\"): %q", line)
			}
			if strings.EqualFold(name, "Authorization") && (hasUser || hasToken) {
				return nil, fmt.Errorf("headers block must not set Authorization when %s/%s or %s is present", credKeyUsername, credKeyPassword, credKeyToken)
			}
			h.Add(name, strings.TrimSpace(value))
		}
		if len(h) == 0 {
			return nil, fmt.Errorf("credential secret has no recognized credential keys (want %s+%s, %s, or %s)",
				credKeyUsername, credKeyPassword, credKeyToken, credKeyHeaders)
		}
	}
	return h, nil
}

// originBoundTransport attaches creds only to requests whose origin
// (scheme+host+port, default ports normalized) equals the archive URL's
// origin. Attach-on-match inside RoundTrip handles redirects naturally: a
// cross-origin hop simply gets a bare request. This extends Go's built-in
// cross-domain Authorization stripping to the custom headers (X-JFrog-Art-Api
// etc.) that net/http would otherwise forward.
type originBoundTransport struct {
	origin *url.URL
	creds  http.Header
	next   http.RoundTripper
}

func (t *originBoundTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if sameOrigin(t.origin, r.URL) {
		r = r.Clone(r.Context())
		for name, values := range t.creds {
			r.Header[name] = append([]string(nil), values...)
		}
	}
	return t.next.RoundTrip(r)
}

// sameOrigin reports whether a and b share scheme+host+port, treating an
// explicit default port (:443 for https, :80 for http) as equal to none —
// https://host and https://host:443 are the same origin semantically.
func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		portOrDefault(a) == portOrDefault(b)
}

func portOrDefault(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// newCredentialClient builds the per-fetch client for a credentialed archive
// URL. Per-fetch on purpose: the credential is scoped to one archive's
// origin, so it must not ride the fetcher's long-lived shared clients.
func newCredentialClient(archiveURL string, creds http.Header) (*http.Client, error) {
	parsed, err := url.Parse(archiveURL)
	if err != nil {
		return nil, fmt.Errorf("invalid archive URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("credentialed archive URL must be http(s), got %q", parsed.Scheme)
	}
	return &http.Client{
		Transport: &originBoundTransport{
			origin: parsed,
			creds:  creds,
			next:   otelhttp.NewTransport(http.DefaultTransport),
		},
	}, nil
}

// clientForArchive picks the download client for a url-type archive: the
// credentialed per-fetch client when SecretRef is set (RFC-0031), else the
// fetcher's shared client selection (signed only for storagesvc URLs). The
// Secret is read from the FETCHING POD's namespace — function-pod namespace
// for deployment archives, builder-pod namespace for source archives —
// mirroring OCIArchive.ImagePullSecrets, and never written to disk.
func (fetcher *Fetcher) clientForArchive(ctx context.Context, archive *fv1.Archive) (*http.Client, error) {
	if archive.SecretRef == nil {
		return fetcher.httpClientForURL(archive.URL), nil
	}
	if parsed, err := url.Parse(archive.URL); err == nil && strings.HasPrefix(parsed.Path, "/v1/archive") {
		// Admission forbids this combination; refuse here too so a stale
		// or hand-crafted object can never mix HMAC and external creds.
		return nil, fmt.Errorf("secretref cannot be used with internal storage service URLs")
	}
	secret, err := fetcher.kubeClient.CoreV1().Secrets(fetcher.Info.Namespace).Get(ctx, archive.SecretRef.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading archive credential secret %s/%s: %w", fetcher.Info.Namespace, archive.SecretRef.Name, err)
	}
	creds, err := parseArchiveCredentials(secret.Data)
	if err != nil {
		return nil, fmt.Errorf("archive credential secret %s/%s: %w", fetcher.Info.Namespace, archive.SecretRef.Name, err)
	}
	return newCredentialClient(archive.URL, creds)
}
