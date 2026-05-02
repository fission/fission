//go:build integration

package framework

import (
	"context"
	"testing"
)

// RouteOptions are the inputs to TestNamespace.CreateRoute.
type RouteOptions struct {
	// Name of the HTTPTrigger. Optional; CLI auto-generates if empty.
	Name string
	// Function name to route to. Required.
	Function string
	// URL path (e.g. "/hello"). Required.
	URL string
	// Method, e.g. "GET". Required.
	Method string
}

// CreateRoute creates an HTTPTrigger that routes Method+URL to the named
// Function. Equivalent to `fission route create`.
func (ns *TestNamespace) CreateRoute(t *testing.T, ctx context.Context, opts RouteOptions) {
	t.Helper()
	if opts.Function == "" || opts.URL == "" || opts.Method == "" {
		t.Fatalf("CreateRoute: Function, URL, and Method are required (got %+v)", opts)
	}
	args := []string{"route", "create",
		"--function", opts.Function,
		"--url", opts.URL,
		"--method", opts.Method,
	}
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	ns.CLI(t, ctx, args...)
}
