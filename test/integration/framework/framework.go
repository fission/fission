// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package framework is the test harness for Go integration tests that run
// against a real Fission deployment on a Kubernetes cluster.
//
// See docs/test-migration/00-design.md for the design and
// docs/test-migration/02-framework-api.md for helper-by-helper docs.
package framework

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	portlessk8s "github.com/sanketsudake/go-portless/k8s"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// Route names the framework registers in its portless registry. Dials to
// these names (via HTTPClient / Router) port-forward in-process to the
// corresponding Service — no kubectl port-forward needed — and block until
// the backend accepts, so readiness lives in the dial.
const (
	// RouterName resolves to svc/router (public listener, Service port 80).
	RouterName = "router.fission"
	// RouterInternalName resolves to svc/router-internal — the ClusterIP-only
	// Service hosting /fission-function/<ns>/<name> after the
	// GHSA-3g33-6vg6-27m8 listener split.
	RouterInternalName = "router-internal.fission"
	// MCPName resolves to svc/mcp (RFC-0011). MCP tests skip when it is
	// unreachable (MCP disabled in this install).
	MCPName = "mcp.fission"
	// StateSvcName resolves to svc/statesvc (RFC-0023 keyed state). State
	// tests skip when it is unreachable (functionState disabled).
	StateSvcName = "statesvc.fission"
)

// Framework is a process-wide singleton built from KUBECONFIG once and reused
// across every test in the package. Per-test isolation is provided by
// NewTestNamespace, not by separate Framework instances.
type Framework struct {
	restConfig    *rest.Config
	clientGen     *crd.ClientGenerator
	fissionClient versioned.Interface
	kubeClient    kubernetes.Interface
	images        RuntimeImages
	// internalAuthSecret is the master HMAC key used to sign
	// /fission-function/... requests on the internal listener.
	// Sourced from FISSION_INTERNAL_AUTH_SECRET; empty disables
	// signing (matches the chart's pass-through mode when
	// internalAuth.enabled=false).
	internalAuthSecret []byte
	// httpClient and routerHTTP are built once so every caller shares one
	// registry transport (and its connection pool) instead of constructing
	// a transport per call. httpClient is unsigned with no global timeout;
	// routerHTTP signs /fission-function/... (when the secret is set) and
	// keeps a 30s per-request timeout.
	httpClient *http.Client
	routerHTTP *http.Client
	logger     logr.Logger
}

var (
	frameworkOnce sync.Once
	framework     *Framework
	frameworkErr  error
)

// Connect returns the framework singleton, building it from KUBECONFIG on first
// call. Subsequent calls return the cached instance.
func Connect(t *testing.T) *Framework {
	t.Helper()
	frameworkOnce.Do(func() {
		framework, frameworkErr = newFramework()
	})
	require.NoError(t, frameworkErr, "framework init")
	return framework
}

func newFramework() (*Framework, error) {
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	clientGen := crd.NewClientGeneratorWithRestConfig(restConfig)
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return nil, err
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return nil, err
	}
	reg, err := newRegistry(restConfig)
	if err != nil {
		return nil, err
	}
	secret := internalAuthSecretFromEnv()
	// Both clients ride the registry's memoized DefaultTransport (one
	// connection pool); its DialContext keeps the registry alive for the
	// process — the registry (and its port-forward connections) is never
	// closed, dying with the test process.
	var signing http.RoundTripper = reg.DefaultTransport()
	if len(secret) > 0 {
		// Sign requests to /fission-function/... with the
		// ServiceRouterInternal key; other paths (HTTPTriggers,
		// /router-healthz) pass through unsigned to match end-user
		// behaviour against the public listener. Shared with the benchmark
		// harness via pkg/auth/hmac.
		signing = hmacauth.NewServiceSigningTransport(secret, hmacauth.ServiceRouterInternal, reg.DefaultTransport(), "/fission-function/")
	}
	return &Framework{
		restConfig:         restConfig,
		clientGen:          clientGen,
		fissionClient:      fissionClient,
		kubeClient:         kubeClient,
		images:             loadRuntimeImages(),
		internalAuthSecret: secret,
		httpClient:         reg.DefaultClient(),
		// WrapRoundTripper applies per-route Host rewrites (the mcp route
		// declares one) on top of the signing wrapper; HMAC canonicals
		// cover method+path+body, not Host, so the order is cosmetic.
		routerHTTP: &http.Client{Timeout: 30 * time.Second, Transport: reg.WrapRoundTripper(signing)},
		logger:     loggerfactory.GetLogger(),
	}, nil
}

// newRegistry builds the portless registry backing all framework HTTP
// clients. Each service defaults to an in-process port-forward
// (self-healing: every dial re-resolves a ready pod, so a pod restart costs
// one retried dial, not a severed tunnel); setting the env override points
// the route at a fixed address instead, for runs against a non-default
// install or a hand-managed forward.
func newRegistry(restConfig *rest.Config) (*portless.Registry, error) {
	namespace := os.Getenv("FISSION_NAMESPACE")
	if namespace == "" {
		namespace = "fission"
	}
	// The registry is strict (the v0.2 default): the route set is closed,
	// and the names deliberately mirror in-cluster DNS shortnames
	// (router.fission = <svc>.<ns>) — a fallback would let a typo'd name
	// silently dial real DNS, which could even succeed from inside a pod.
	reg := portless.New()
	for _, r := range []struct {
		name, envVar, service string
		opts                  []portless.RouteOption
	}{
		{RouterName, "FISSION_ROUTER", "router", nil},
		{RouterInternalName, "FISSION_ROUTER_INTERNAL", "router-internal", nil},
		// The MCP SDK's DNS-rebinding heuristic 403s "loopback peer +
		// non-loopback Host" — which is exactly what a port-forwarded dial
		// carrying Host: mcp.fission looks like. Present a loopback Host
		// instead of weakening the server's protection.
		{MCPName, "FISSION_MCP_BASE_URL", "mcp", []portless.RouteOption{portless.RouteWithHostRewrite("127.0.0.1")}},
		{StateSvcName, "FISSION_STATESVC", "statesvc", nil},
	} {
		var b portless.Backend
		var err error
		if v := os.Getenv(r.envVar); v != "" {
			b, err = backend.ParseTCP(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", r.envVar, err)
			}
		} else {
			// No explicit target port: all three Services are single-port,
			// so the resolver uses the Service's own target port.
			b, err = portlessk8s.PortForward(restConfig, portlessk8s.Service(namespace, r.service))
			if err != nil {
				return nil, fmt.Errorf("build %s backend: %w", r.name, err)
			}
		}
		if _, err := reg.Add(context.Background(), r.name, b, r.opts...); err != nil {
			return nil, fmt.Errorf("register route %s: %w", r.name, err)
		}
	}
	return reg, nil
}

// RestConfig returns the cached *rest.Config.
func (f *Framework) RestConfig() *rest.Config { return f.restConfig }

// FissionClient returns the typed Fission clientset.
func (f *Framework) FissionClient() versioned.Interface { return f.fissionClient }

// KubeClient returns the Kubernetes clientset.
func (f *Framework) KubeClient() kubernetes.Interface { return f.kubeClient }

// Images returns the runtime/builder image registry resolved from environment.
func (f *Framework) Images() RuntimeImages { return f.images }

// Logger returns the framework logger.
func (f *Framework) Logger() logr.Logger { return f.logger }

// HTTPClient returns the shared http.Client that resolves the framework's
// route names (RouterName etc.) through the portless registry. It sets no
// global timeout — dials block until the backend is ready — so callers must
// bound requests with contexts. Requests through it are NOT HMAC-signed; use
// Router for signed /fission-function/... calls.
func (f *Framework) HTTPClient() *http.Client { return f.httpClient }

// RouterInternalBaseURL returns the framework's URL for the router's
// internal listener — the one hosting /fission-function/<ns>/<name>
// after the GHSA-3g33-6vg6-27m8 split. The host is a portless route name,
// resolvable only by clients built from this framework (HTTPClient, Router).
func (f *Framework) RouterInternalBaseURL() string {
	return portless.URL(RouterInternalName, 0, "")
}

// StateSvcBaseURL returns the framework's URL for the statesvc keyed-state
// head (RFC-0023). The host is a portless route name, resolvable only by
// clients built from this framework.
func (f *Framework) StateSvcBaseURL() string {
	return portless.URL(StateSvcName, 0, "")
}

// IsTargetMissing reports whether a client/dial error means the route's
// Kubernetes target (Service/pod) does not exist at all — as opposed to
// existing but not ready yet. Tests for optional subsystems use it to skip
// fast instead of waiting out the full readiness deadline.
func IsTargetMissing(err error) bool {
	return errors.Is(err, portlessk8s.ErrTargetNotFound)
}

// RouterInternalWSURL returns a ws:// URL for `path` on the router's internal
// listener, for websocket dials that carry HTTPClient through the upgrade
// handshake (coder/websocket DialOptions.HTTPClient).
func (f *Framework) RouterInternalWSURL(path string) string {
	return portless.WSURL(RouterInternalName, 0, path)
}

// InternalAuthSecret returns the master HMAC key the framework uses
// to sign /fission-function/... requests on the internal listener.
// Empty when internalAuth is disabled in the cluster — callers should
// emit unsigned requests in that case (the verifier short-circuits to
// pass-through).
func (f *Framework) InternalAuthSecret() []byte { return f.internalAuthSecret }

// MCPBaseURL returns the URL of the MCP server (svc/mcp). MCP integration tests
// dial "<base>/mcp" via HTTPClient; they should skip when the endpoint is
// unreachable (the MCP subsystem is enabled in the kind/kind-ci skaffold
// profiles but may be off in other installs).
func (f *Framework) MCPBaseURL() string { return portless.URL(MCPName, 0, "") }
