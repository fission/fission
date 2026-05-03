//go:build integration

package framework

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
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
	// Resource defaults the env applies to its spawned pods (millicores /
	// MiB). Functions can override per-fn via FunctionOptions.{Min,Max}{CPU,Memory}.
	MinCPU    int
	MaxCPU    int
	MinMemory int
	MaxMemory int
	// Poolsize controls the warm pool size (poolmgr only).
	Poolsize int
	// Period, when > 0, is passed as `--period <n>` (seconds). This is
	// the env's polling/check interval — lower values speed up the
	// idle-pod reaper for tests that want to observe scale-down quickly.
	Period int
}

// CreateEnvObject creates a Fission Environment from a fully-formed CR
// object via the typed clientset (not the CLI), and registers its deletion on
// the namespace cleanup chain. Use this for tests that need fields the CLI
// doesn't expose — e.g. metadata.annotations (TestAnnotations) or
// runtime.podspec for pod-level customization. Forces env.Namespace to ns.Name.
func (ns *TestNamespace) CreateEnvObject(t *testing.T, ctx context.Context, env *fv1.Environment) {
	t.Helper()
	require.NotEmpty(t, env.Name, "env.Name")
	env.Namespace = ns.Name
	_, err := ns.f.fissionClient.CoreV1().Environments(ns.Name).Create(ctx, env, metav1.CreateOptions{})
	require.NoErrorf(t, err, "create environment %q", env.Name)
	name := env.Name
	ns.addCleanup("env "+name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().Environments(ns.Name).Delete(c, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}

// CreateEnv creates a Fission Environment via the CLI and registers its
// deletion on the namespace's cleanup chain (which runs after the diagnostics
// dump on failure).
func (ns *TestNamespace) CreateEnv(t *testing.T, ctx context.Context, opts EnvOptions) {
	t.Helper()
	require.NotEmpty(t, opts.Name, "EnvOptions.Name")
	require.NotEmpty(t, opts.Image, "EnvOptions.Image")

	args := []string{"env", "create", "--name", opts.Name, "--image", opts.Image}
	if opts.Builder != "" {
		args = append(args, "--builder", opts.Builder)
	}
	if opts.GracePeriod > 0 {
		args = append(args, "--graceperiod", strconv.Itoa(opts.GracePeriod))
	}
	if opts.MinCPU > 0 {
		args = append(args, "--mincpu", strconv.Itoa(opts.MinCPU))
	}
	if opts.MaxCPU > 0 {
		args = append(args, "--maxcpu", strconv.Itoa(opts.MaxCPU))
	}
	if opts.MinMemory > 0 {
		args = append(args, "--minmemory", strconv.Itoa(opts.MinMemory))
	}
	if opts.MaxMemory > 0 {
		args = append(args, "--maxmemory", strconv.Itoa(opts.MaxMemory))
	}
	if opts.Poolsize > 0 {
		args = append(args, "--poolsize", strconv.Itoa(opts.Poolsize))
	}
	if opts.Period > 0 {
		args = append(args, "--period", strconv.Itoa(opts.Period))
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("env "+opts.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().Environments(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})

	// For builder envs, the very next step is usually a source-archive
	// package build. The buildermgr POSTs to the runtime pod's fetcher
	// on port 8000; if the pod is still in ContainerCreating the call
	// times out (`dial tcp ...:8000: i/o timeout`). Pre-wait here so
	// individual tests don't have to remember.
	if opts.Builder != "" {
		ns.WaitForEnvReady(t, ctx, opts.Name)
	}
}
