// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"net/http"
	"strings"
	"time"
)

// NewServiceSigningTransport returns an http.RoundTripper that signs requests
// whose URL path begins with pathPrefix using the key derived from master for
// service, and forwards every other request through inner unmodified. An empty
// pathPrefix signs all requests.
//
// The service key is derived once (HKDF) and the signer reused, so it is cheap
// on a hot path such as a load generator. An empty master yields pass-through
// signing (no headers added), matching the verifier's disabled-internalAuth
// behaviour. If inner is nil, http.DefaultTransport is used.
//
// It centralises the path-gated router-internal signing that the integration
// framework and the benchmark harness would otherwise each reimplement.
func NewServiceSigningTransport(master []byte, service Service, inner http.RoundTripper, pathPrefix string) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &serviceSigningTransport{
		signer: ServiceSigner(master, service, inner, time.Now),
		inner:  inner,
		prefix: pathPrefix,
	}
}

type serviceSigningTransport struct {
	signer *Signer
	inner  http.RoundTripper
	prefix string
}

func (t *serviceSigningTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.prefix != "" && !strings.HasPrefix(r.URL.Path, t.prefix) {
		return t.inner.RoundTrip(r)
	}
	return t.signer.RoundTrip(r)
}
