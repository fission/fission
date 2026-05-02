//go:build integration

package framework

import (
	"context"
	"testing"
)

// EnvOptions are the inputs to TestNamespace.CreateEnv.
type EnvOptions struct {
	// Name of the Environment CR. Required.
	Name string
	// Image is the runtime image (e.g. NODE_RUNTIME_IMAGE). Required.
	Image string
}

// CreateEnv creates a Fission Environment via the CLI. The environment is
// scoped to the test namespace and cleaned up automatically when the namespace
// is deleted at test end.
func (ns *TestNamespace) CreateEnv(t *testing.T, ctx context.Context, opts EnvOptions) {
	t.Helper()
	if opts.Name == "" || opts.Image == "" {
		t.Fatalf("CreateEnv: Name and Image are required (got %+v)", opts)
	}
	ns.CLI(t, ctx, "env", "create", "--name", opts.Name, "--image", opts.Image)
}
