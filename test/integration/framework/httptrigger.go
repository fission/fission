//go:build integration

package framework

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FunctionWeight pairs a Function name with its routing weight (0–100), for
// canary-style HTTPTriggers that fan traffic across multiple function versions.
type FunctionWeight struct {
	Name   string
	Weight int
}

// RouteOptions are the inputs to TestNamespace.CreateRoute.
type RouteOptions struct {
	// Name of the HTTPTrigger. If empty, the framework derives a stable name
	// from Function so cleanup can find it without parsing CLI output.
	Name string
	// URL path (e.g. "/hello"). Required.
	URL string
	// Method, e.g. "GET". Required.
	Method string

	// Function is the single backing function name. Either Function or
	// FunctionWeights must be set, not both.
	Function string

	// FunctionWeights is the canary form: multiple weighted functions on a
	// single trigger. The CLI accepts paired `--function <fn> --weight <w>`
	// in the same order.
	FunctionWeights []FunctionWeight
}

// CreateRoute creates an HTTPTrigger via the CLI. A stable trigger name is
// derived from the (first) Function so cleanup can find it without parsing
// CLI output.
func (ns *TestNamespace) CreateRoute(t *testing.T, ctx context.Context, opts RouteOptions) {
	t.Helper()
	if opts.URL == "" || opts.Method == "" {
		t.Fatalf("CreateRoute: URL and Method are required (got %+v)", opts)
	}
	if (opts.Function == "") == (len(opts.FunctionWeights) == 0) {
		t.Fatalf("CreateRoute: exactly one of Function or FunctionWeights must be set (got %+v)", opts)
	}
	if opts.Name == "" {
		switch {
		case opts.Function != "":
			opts.Name = "route-" + opts.Function
		default:
			opts.Name = "route-" + opts.FunctionWeights[0].Name
		}
	}
	args := []string{"route", "create", "--name", opts.Name, "--url", opts.URL, "--method", opts.Method}
	if opts.Function != "" {
		args = append(args, "--function", opts.Function)
	} else {
		for _, fw := range opts.FunctionWeights {
			args = append(args, "--function", fw.Name, "--weight", strconv.Itoa(fw.Weight))
		}
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("route "+opts.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().HTTPTriggers(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}

// FunctionWeight returns the current routing weight assigned to fnName on the
// named HTTPTrigger, or 0 if the function isn't listed in functionref.
// functionweights. Tests typically poll this via WaitForFunctionWeight.
func (ns *TestNamespace) FunctionWeight(t *testing.T, ctx context.Context, routeName, fnName string) int {
	t.Helper()
	tr, err := ns.f.fissionClient.CoreV1().HTTPTriggers(ns.Name).Get(ctx, routeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("FunctionWeight: get httptrigger %q: %v", routeName, err)
	}
	if tr.Spec.FunctionReference.FunctionWeights == nil {
		return 0
	}
	return tr.Spec.FunctionReference.FunctionWeights[fnName]
}

// WaitForFunctionWeight polls until the routing weight assigned to fnName on
// the named HTTPTrigger reaches `want`, or the timeout elapses. Use this to
// observe the canary controller's traffic-shift decisions.
func (ns *TestNamespace) WaitForFunctionWeight(t *testing.T, ctx context.Context, routeName, fnName string, want int, timeout time.Duration) {
	t.Helper()
	ns.waitForFunctionWeightCond(t, ctx, routeName, fnName, timeout,
		func(w int) bool { return w == want },
		fmt.Sprintf("==%d", want))
}

// WaitForFunctionWeightAtLeast polls until the weight assigned to fnName on
// routeName is >= want. Use this to observe the canary controller making
// *some* progress before checking the final state — e.g. for rollback tests,
// confirm v3 weight first rose above 0 before asserting it returned to 0.
func (ns *TestNamespace) WaitForFunctionWeightAtLeast(t *testing.T, ctx context.Context, routeName, fnName string, want int, timeout time.Duration) {
	t.Helper()
	ns.waitForFunctionWeightCond(t, ctx, routeName, fnName, timeout,
		func(w int) bool { return w >= want },
		fmt.Sprintf(">=%d", want))
}

func (ns *TestNamespace) waitForFunctionWeightCond(
	t *testing.T,
	ctx context.Context,
	routeName, fnName string,
	timeout time.Duration,
	match func(int) bool,
	desc string,
) {
	t.Helper()
	var last int
	EventuallyLazy(t, ctx, timeout, 2*time.Second,
		func(c context.Context) (bool, error) {
			tr, err := ns.f.fissionClient.CoreV1().HTTPTriggers(ns.Name).Get(c, routeName, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if tr.Spec.FunctionReference.FunctionWeights != nil {
				last = tr.Spec.FunctionReference.FunctionWeights[fnName]
			}
			return match(last), nil
		},
		func() string {
			return fmt.Sprintf("route %q: weight for function %q never satisfied %s (last=%d)", routeName, fnName, desc, last)
		})
}
