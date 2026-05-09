//go:build integration

// Package framework is the test harness for Go integration tests that run
// against a real Fission deployment on a Kubernetes cluster.
//
// See docs/test-migration/00-design.md for the design and
// docs/test-migration/02-framework-api.md for helper-by-helper docs.
package framework

import (
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils/loggerfactory"
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
	router        string
	// routerInternal is the URL the test framework uses for the
	// router's internal listener (the one hosting /fission-function/...
	// after the GHSA-3g33-6vg6-27m8 split). Sourced from
	// FISSION_ROUTER_INTERNAL and defaults to http://127.0.0.1:8889 to
	// match the suite-bootstrap port-forward of svc/router-internal.
	// Always non-empty after framework setup; the RouterClient
	// unconditionally routes /fission-function/<ns>/<name> here.
	routerInternal string
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
	return &Framework{
		restConfig:         restConfig,
		clientGen:          clientGen,
		fissionClient:      fissionClient,
		kubeClient:         kubeClient,
		images:             loadRuntimeImages(),
		router:             routerURLFromEnv(),
		routerInternal:     routerInternalURLFromEnv(),
		internalAuthSecret: internalAuthSecretFromEnv(),
		logger:             loggerfactory.GetLogger(),
	}, nil
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

// RouterInternalBaseURL returns the framework's URL for the router's
// internal listener — the one hosting /fission-function/<ns>/<name>
// after the GHSA-3g33-6vg6-27m8 split. Tests that bypass the
// `RouterClient` HTTP helpers (e.g. websocket dials done via
// gorilla/websocket) should compose their URL from this base.
func (f *Framework) RouterInternalBaseURL() string { return f.routerInternal }

// InternalAuthSecret returns the master HMAC key the framework uses
// to sign /fission-function/... requests on the internal listener.
// Empty when internalAuth is disabled in the cluster — callers should
// emit unsigned requests in that case (the verifier short-circuits to
// pass-through).
func (f *Framework) InternalAuthSecret() []byte { return f.internalAuthSecret }
