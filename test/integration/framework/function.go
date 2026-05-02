//go:build integration

package framework

import (
	"context"
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

// CreateFunction creates a Function via the CLI from a local code file.
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
