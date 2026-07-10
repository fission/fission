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
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	portlessk8s "github.com/sanketsudake/go-portless/k8s"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

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
	// reg resolves the route names above. Each route is either an
	// in-process SPDY port-forward to the Service's ready pod (default) or
	// a fixed TCP address when the matching env override (FISSION_ROUTER /
	// FISSION_ROUTER_INTERNAL / FISSION_MCP_BASE_URL) is set. The registry
	// lives for the whole test process; its connections die with it.
	reg *portless.Registry
	// internalAuthSecret is the master HMAC key used to sign
	// /fission-function/... requests on the internal listener.
	// Sourced from FISSION_INTERNAL_AUTH_SECRET; empty disables
	// signing (matches the chart's pass-through mode when
	// internalAuth.enabled=false).
	internalAuthSecret []byte
	logger             logr.Logger
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
	return &Framework{
		restConfig:         restConfig,
		clientGen:          clientGen,
		fissionClient:      fissionClient,
		kubeClient:         kubeClient,
		images:             loadRuntimeImages(),
		reg:                reg,
		internalAuthSecret: internalAuthSecretFromEnv(),
		logger:             loggerfactory.GetLogger(),
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
	reg := portless.New()
	for _, r := range []struct{ name, envVar, service string }{
		{RouterName, "FISSION_ROUTER", "router"},
		{RouterInternalName, "FISSION_ROUTER_INTERNAL", "router-internal"},
		{MCPName, "FISSION_MCP_BASE_URL", "mcp"},
	} {
		var b portless.Backend
		if v := os.Getenv(r.envVar); v != "" {
			b = backend.TCP(hostport(v))
		} else {
			var err error
			// No explicit target port: all three Services are single-port,
			// so the resolver uses the Service's own target port.
			b, err = portlessk8s.PortForward(restConfig, portlessk8s.Service(namespace, r.service))
			if err != nil {
				return nil, fmt.Errorf("build %s backend: %w", r.name, err)
			}
		}
		if _, err := reg.Add(context.Background(), r.name, b); err != nil {
			return nil, fmt.Errorf("register route %s: %w", r.name, err)
		}
	}
	return reg, nil
}

// hostport normalizes an env override ("host:port", "http://host:port", or
// bare "host") to the host:port form backend.TCP dials.
func hostport(v string) string {
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimSuffix(v, "/")
	if !strings.Contains(v, ":") {
		v += ":80"
	}
	return v
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

// HTTPClient returns an http.Client that resolves the framework's route
// names (RouterName etc.) through the portless registry. It sets no global
// timeout — dials block until the backend is ready — so callers must bound
// requests with contexts. Requests through it are NOT HMAC-signed; use
// Router for signed /fission-function/... calls.
func (f *Framework) HTTPClient() *http.Client { return f.reg.HTTPClient() }

// RouterInternalBaseURL returns the framework's URL for the router's
// internal listener — the one hosting /fission-function/<ns>/<name>
// after the GHSA-3g33-6vg6-27m8 split. The host is a portless route name,
// resolvable only by clients built from this framework (HTTPClient, Router).
func (f *Framework) RouterInternalBaseURL() string {
	return portless.URL(RouterInternalName, 0, "")
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
