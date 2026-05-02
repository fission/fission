//go:build integration

package framework

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RouteOptions are the inputs to TestNamespace.CreateRoute.
type RouteOptions struct {
	// Name of the HTTPTrigger. If empty, the framework derives a stable name
	// from Function so cleanup is deterministic.
	Name string
	// Function name to route to. Required.
	Function string
	// URL path (e.g. "/hello"). Required.
	URL string
	// Method, e.g. "GET". Required.
	Method string
}

// CreateRoute creates an HTTPTrigger via the CLI. A stable trigger name is
// derived from the Function so cleanup can find it without parsing CLI output.
func (ns *TestNamespace) CreateRoute(t *testing.T, ctx context.Context, opts RouteOptions) {
	t.Helper()
	if opts.Function == "" || opts.URL == "" || opts.Method == "" {
		t.Fatalf("CreateRoute: Function, URL, and Method are required (got %+v)", opts)
	}
	if opts.Name == "" {
		opts.Name = "route-" + opts.Function
	}
	ns.CLI(t, ctx, "route", "create",
		"--name", opts.Name,
		"--function", opts.Function,
		"--url", opts.URL,
		"--method", opts.Method,
	)

	t.Cleanup(func() {
		if noCleanup() {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := ns.f.fissionClient.CoreV1().HTTPTriggers(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Logf("cleanup: delete route %q: %v", opts.Name, err)
		}
	})
}
