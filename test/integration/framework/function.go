//go:build integration

package framework

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FunctionOptions are the inputs to TestNamespace.CreateFunction.
type FunctionOptions struct {
	// Name of the Function CR. Required.
	Name string
	// Env is the Environment name to use. Required.
	Env string
	// Code is the local file path to the function source. Required.
	Code string
}

// CreateFunction creates a Function via the CLI from a local code file. The
// CLI also creates a backing Package with a generated name (`<fn>-<uuid>`).
// Cleanup deletes both: the Function explicitly and any Package whose name
// starts with the function name.
func (ns *TestNamespace) CreateFunction(t *testing.T, ctx context.Context, opts FunctionOptions) {
	t.Helper()
	if opts.Name == "" || opts.Env == "" || opts.Code == "" {
		t.Fatalf("CreateFunction: Name, Env, and Code are required (got %+v)", opts)
	}
	ns.CLI(t, ctx, "fn", "create",
		"--name", opts.Name,
		"--env", opts.Env,
		"--code", opts.Code,
	)

	t.Cleanup(func() {
		if noCleanup() {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Logf("cleanup: delete fn %q: %v", opts.Name, err)
		}

		// The CLI auto-generates a Package named `<fn>-<uuid>`. Find and delete it.
		pkgs, err := ns.f.fissionClient.CoreV1().Packages(ns.Name).List(c, metav1.ListOptions{})
		if err != nil {
			t.Logf("cleanup: list packages: %v", err)
			return
		}
		for _, p := range pkgs.Items {
			if strings.HasPrefix(p.Name, opts.Name+"-") {
				if err := ns.f.fissionClient.CoreV1().Packages(ns.Name).Delete(c, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
					t.Logf("cleanup: delete pkg %q: %v", p.Name, err)
				}
			}
		}
	})
}

// WaitForFunction polls until the Function CR exists in the test namespace, or
// the context times out. Use this when the CLI returns before the controller
// has processed the request.
func (ns *TestNamespace) WaitForFunction(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	Eventually(t, ctx, 30*time.Second, 500*time.Millisecond, func(c context.Context) (bool, error) {
		_, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(c, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	}, "function %q not visible in namespace %q", name, ns.Name)
}
