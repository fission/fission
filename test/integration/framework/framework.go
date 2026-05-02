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
	logger        logr.Logger
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
	if frameworkErr != nil {
		t.Fatalf("framework init failed: %v", frameworkErr)
	}
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
		restConfig:    restConfig,
		clientGen:     clientGen,
		fissionClient: fissionClient,
		kubeClient:    kubeClient,
		images:        loadRuntimeImages(),
		router:        routerURLFromEnv(),
		logger:        loggerfactory.GetLogger(),
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
