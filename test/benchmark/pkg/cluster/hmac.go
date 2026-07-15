// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"net/http"

	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/auth/hmac"
	storageclient "github.com/fission/fission/pkg/storagesvc/client"
)

// internalListenerPrefix bounds which request paths the wrapper signs. "/"
// signs everything: the wrapper is only ever applied to internal-listener
// targets, and the verifier covers every internal path — function invocation
// (/fission-function/), the DLQ admin API (/v1/async/dlq/) and the RFC-0027
// topic admin API (/v1/eventing/).
const internalListenerPrefix = "/"

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
// router internal listener with the ServiceRouterInternal-derived key, via the
// shared hmac.NewServiceSigningTransport (the same helper the integration
// framework uses). It returns nil when master is empty so callers can leave the
// transport unwrapped.
func SigningTransportWrapper(master []byte) func(http.RoundTripper) http.RoundTripper {
	if len(master) == 0 {
		return nil
	}
	return func(inner http.RoundTripper) http.RoundTripper {
		return hmac.NewServiceSigningTransport(master, hmac.ServiceRouterInternal, inner, internalListenerPrefix)
	}
}
