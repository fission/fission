//go:build integration

package framework

import (
	"context"
	"strconv"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CanaryConfigOptions are the inputs to TestNamespace.CreateCanaryConfig.
type CanaryConfigOptions struct {
	// Name of the CanaryConfig CR.
	Name string
	// NewFunction is the function being rolled out (will receive growing weight).
	NewFunction string
	// OldFunction is the function being rolled back from.
	OldFunction string
	// HTTPTrigger is the route the canary controls.
	HTTPTrigger string
	// IncrementStep is the weight delta per tick (e.g. 50).
	IncrementStep int
	// IncrementInterval is the tick period as a Go-style duration string
	// the CLI accepts ("1m", "30s").
	IncrementInterval string
	// FailureThreshold is the failure rate (%) above which the canary rolls back.
	FailureThreshold int
}

// CreateCanaryConfig creates a CanaryConfig CR via the CLI. The canary
// controller then ticks every IncrementInterval, advancing weight by
// IncrementStep on success and rolling back when failure rate exceeds
// FailureThreshold.
func (ns *TestNamespace) CreateCanaryConfig(t *testing.T, ctx context.Context, opts CanaryConfigOptions) {
	t.Helper()
	if opts.Name == "" || opts.NewFunction == "" || opts.OldFunction == "" || opts.HTTPTrigger == "" {
		t.Fatalf("CreateCanaryConfig: Name/NewFunction/OldFunction/HTTPTrigger required (got %+v)", opts)
	}
	args := []string{
		"canary-config", "create",
		"--name", opts.Name,
		"--newfunction", opts.NewFunction,
		"--oldfunction", opts.OldFunction,
		"--httptrigger", opts.HTTPTrigger,
	}
	if opts.IncrementStep > 0 {
		args = append(args, "--increment-step", strconv.Itoa(opts.IncrementStep))
	}
	if opts.IncrementInterval != "" {
		args = append(args, "--increment-interval", opts.IncrementInterval)
	}
	if opts.FailureThreshold > 0 {
		args = append(args, "--failure-threshold", strconv.Itoa(opts.FailureThreshold))
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("canary-config "+opts.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().CanaryConfigs(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}
