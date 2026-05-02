//go:build integration

package framework

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnvOptions are the inputs to TestNamespace.CreateEnv.
type EnvOptions struct {
	// Name of the Environment CR. Required.
	Name string
	// Image is the runtime image (e.g. NODE_RUNTIME_IMAGE). Required.
	Image string
}

// CreateEnv creates a Fission Environment via the CLI and registers a
// t.Cleanup that deletes it via the typed Fission clientset.
func (ns *TestNamespace) CreateEnv(t *testing.T, ctx context.Context, opts EnvOptions) {
	t.Helper()
	if opts.Name == "" || opts.Image == "" {
		t.Fatalf("CreateEnv: Name and Image are required (got %+v)", opts)
	}
	ns.CLI(t, ctx, "env", "create", "--name", opts.Name, "--image", opts.Image)

	t.Cleanup(func() {
		if noCleanup() {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := ns.f.fissionClient.CoreV1().Environments(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Logf("cleanup: delete env %q: %v", opts.Name, err)
		}
	})
}
