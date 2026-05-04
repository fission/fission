//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestNamespaceFlag is the Go port of test_namespace/test_ns_flag.sh.
// Creates a fresh namespace via the Kubernetes API, then creates an
// HTTPTrigger in that namespace via `fission httptrigger create
// --namespace <ns>` and verifies the CR exists there via the typed
// Fission clientset.
//
// The bash test creates the trigger referencing a non-existent function;
// we do the same — the assertion is purely on the trigger CR, not on the
// router serving the URL.
func TestNamespaceFlag(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	customNS := "fission-it-ns-flag-" + ns.ID
	createKubeNamespace(t, ctx, f, customNS)

	trigger := "http-" + ns.ID
	url := "/url-" + ns.ID
	ns.CLI(t, ctx, "httptrigger", "create",
		"--function", "nbuilderhello-"+ns.ID,
		"--url", url, "--name", trigger,
		"--namespace", customNS)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = f.FissionClient().CoreV1().HTTPTriggers(customNS).Delete(ctx, trigger, metav1.DeleteOptions{})
	})

	tr, err := f.FissionClient().CoreV1().HTTPTriggers(customNS).Get(ctx, trigger, metav1.GetOptions{})
	require.NoErrorf(t, err, "get httptrigger %q in ns %q", trigger, customNS)
	assert.Equal(t, customNS, tr.Namespace)
}

// TestNamespaceCurrentContext is the Go port of test_namespace/test_ns_current_context.sh.
// Without --namespace, the CLI defaults to the kubeconfig's current
// namespace — which in our framework is always `default` (we set
// ClientOptions.Namespace = "default" in cli.go). The trigger should land
// in `default` and be findable there.
func TestNamespaceCurrentContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	trigger := "http-cc-" + ns.ID
	url := "/url-cc-" + ns.ID
	ns.CLI(t, ctx, "httptrigger", "create",
		"--function", "nbuilderhello-"+ns.ID,
		"--url", url, "--name", trigger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = f.FissionClient().CoreV1().HTTPTriggers(metav1.NamespaceDefault).Delete(ctx, trigger, metav1.DeleteOptions{})
	})

	tr, err := f.FissionClient().CoreV1().HTTPTriggers(metav1.NamespaceDefault).Get(ctx, trigger, metav1.GetOptions{})
	require.NoErrorf(t, err, "get httptrigger %q in default ns", trigger)
	assert.Equal(t, metav1.NamespaceDefault, tr.Namespace)
}

// TestNamespaceDeprecatedFlag is the Go port of test_namespace/test_ns_deprecated_flag.sh.
// Verifies that the deprecated `--fnNamespace` flag is still honored on
// `fission httptrigger create` (it's marked Deprecated in the CLI flag
// definition with `Substitute: --namespace`, so the create call should
// succeed and the trigger should land in the named namespace).
func TestNamespaceDeprecatedFlag(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	customNS := "fission-it-ns-deprflag-" + ns.ID
	createKubeNamespace(t, ctx, f, customNS)

	trigger := "http-deprfl-" + ns.ID
	url := "/url-deprfl-" + ns.ID
	ns.CLI(t, ctx, "httptrigger", "create",
		"--function", "nbuilderhello-"+ns.ID,
		"--url", url, "--name", trigger,
		"--fnNamespace", customNS)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = f.FissionClient().CoreV1().HTTPTriggers(customNS).Delete(ctx, trigger, metav1.DeleteOptions{})
	})

	tr, err := f.FissionClient().CoreV1().HTTPTriggers(customNS).Get(ctx, trigger, metav1.GetOptions{})
	require.NoErrorf(t, err, "get httptrigger %q in ns %q", trigger, customNS)
	assert.Equal(t, customNS, tr.Namespace)
}

// TestNamespaceEnv is the Go port of test_namespace/test_ns_env.sh.
// Verifies that FISSION_DEFAULT_NAMESPACE is honored when neither
// `--namespace` nor the deprecated `--fnNamespace` is passed. The CLI's
// resolution (pkg/fission-cli/cmd/cmd.go GetResourceNamespace) walks
// flag → flag → env-var → ClientOptions.Namespace, so the env-var
// branch wins over our default ClientOptions.Namespace="default".
//
// Uses the framework's CLIWithEnv helper which serializes the env-var
// mutation against any other in-flight CLI calls via cliMu.
func TestNamespaceEnv(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	customNS := "fission-it-ns-env-" + ns.ID
	createKubeNamespace(t, ctx, f, customNS)

	trigger := "http-env-" + ns.ID
	url := "/url-env-" + ns.ID
	ns.CLIWithEnv(t, ctx,
		map[string]string{"FISSION_DEFAULT_NAMESPACE": customNS},
		"httptrigger", "create",
		"--function", "nbuilderhello-"+ns.ID,
		"--url", url, "--name", trigger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = f.FissionClient().CoreV1().HTTPTriggers(customNS).Delete(ctx, trigger, metav1.DeleteOptions{})
	})

	tr, err := f.FissionClient().CoreV1().HTTPTriggers(customNS).Get(ctx, trigger, metav1.GetOptions{})
	require.NoErrorf(t, err, "get httptrigger %q in ns %q", trigger, customNS)
	assert.Equal(t, customNS, tr.Namespace)
}

// createKubeNamespace creates a Kubernetes namespace and registers a
// t.Cleanup that deletes it. Helper local to the namespace tests.
func createKubeNamespace(t *testing.T, ctx context.Context, f *framework.Framework, name string) {
	t.Helper()
	_, err := f.KubeClient().CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	require.NoErrorf(t, err, "create ns %q", name)
	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := f.KubeClient().CoreV1().Namespaces().Delete(dctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Logf("cleanup: delete ns %q: %v", name, err)
		}
	})
}
