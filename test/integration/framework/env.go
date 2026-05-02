//go:build integration

package framework

import (
	"context"
	"strconv"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnvOptions are the inputs to TestNamespace.CreateEnv.
type EnvOptions struct {
	// Name of the Environment CR. Required.
	Name string
	// Image is the runtime image (e.g. NODE_RUNTIME_IMAGE). Required.
	Image string
	// Builder is the optional builder image (e.g. PYTHON_BUILDER_IMAGE). When
	// set, source-package functions can be built against this environment.
	Builder string
	// GracePeriod, when > 0, is passed as `--graceperiod <n>` (seconds).
	// Lower values speed up pod recycling between function versions —
	// useful for canary tests that need traffic to flip quickly.
	GracePeriod int
}

// CreateEnv creates a Fission Environment via the CLI and registers its
// deletion on the namespace's cleanup chain (which runs after the diagnostics
// dump on failure).
func (ns *TestNamespace) CreateEnv(t *testing.T, ctx context.Context, opts EnvOptions) {
	t.Helper()
	if opts.Name == "" || opts.Image == "" {
		t.Fatalf("CreateEnv: Name and Image are required (got %+v)", opts)
	}
	args := []string{"env", "create", "--name", opts.Name, "--image", opts.Image}
	if opts.Builder != "" {
		args = append(args, "--builder", opts.Builder)
	}
	if opts.GracePeriod > 0 {
		args = append(args, "--graceperiod", strconv.Itoa(opts.GracePeriod))
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("env "+opts.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().Environments(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}
