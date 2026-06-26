// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"net/http"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/auth/hmac"
	storageclient "github.com/fission/fission/pkg/storagesvc/client"
)

// InternalAuthSecret resolves the router-internal HMAC master secret: the
// FISSION_INTERNAL_AUTH_SECRET env var wins, else the in-cluster
// fission-internal-auth Secret. Returns nil when neither is present, in which
// case the router internal listener runs in pass-through mode and requests need
// no signing.
func InternalAuthSecret(ctx context.Context, kube kubernetes.Interface, fissionNamespace string) ([]byte, error) {
	if s := storageclient.HMACSecretFromEnv(); len(s) > 0 {
		return s, nil
	}
	return storageclient.HMACSecretFromCluster(ctx, kube, fissionNamespace)
}

// SigningTransportWrapper returns a transport wrapper that signs requests to the
// router internal listener (paths under /fission-function/) with the
// ServiceRouterInternal-derived key, mirroring the integration framework. It
// returns nil when master is empty so callers can leave the transport
// unwrapped.
func SigningTransportWrapper(master []byte) func(http.RoundTripper) http.RoundTripper {
	if len(master) == 0 {
		return nil
	}
	return func(inner http.RoundTripper) http.RoundTripper {
		// Derive the key + signer once per target (HKDF is deterministic), not
		// per request — per-request derivation would contaminate the very
		// latencies the load generator measures.
		return &signingTransport{
			signer: hmac.ServiceSigner(master, hmac.ServiceRouterInternal, inner, time.Now),
			inner:  inner,
		}
	}
}

type signingTransport struct {
	signer *hmac.Signer
	inner  http.RoundTripper
}

func (t *signingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(r.URL.Path, "/fission-function/") {
		return t.inner.RoundTrip(r)
	}
	return t.signer.RoundTrip(r)
}
