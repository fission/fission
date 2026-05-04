//go:build integration

package framework

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// FunctionOptions are the inputs to TestNamespace.CreateFunction.
//
// Exactly one of Code (single file), Src (zipped source package), or Pkg
// (existing Package name) must be provided. When Src is set, Entrypoint and
// BuildCmd describe how the builder produces the deploy archive. When Pkg is
// set, Env may be omitted (the package already references its env).
type FunctionOptions struct {
	// Name of the Function CR. Required.
	Name string
	// Env is the Environment name to use. Required unless Pkg is set.
	Env string
	// Code is the local file path to a single source file (e.g. hello.js).
	Code string
	// Src is the local file path to a source archive (e.g. .zip). The
	// environment must have a Builder configured.
	Src string
	// Deploy is the local file path to a *deploy* archive — already built,
	// no builder invocation. Mutually exclusive with Code/Src/Pkg.
	Deploy string
	// Pkg is the name of an existing Package CR. The Function references
	// the package by name; the env is inherited from the package.
	Pkg string
	// Entrypoint identifies the function in the source archive or package
	// (e.g. "user.main").
	Entrypoint string
	// BuildCmd is the command run by the builder (e.g. "./build.sh").
	BuildCmd string
	// FnTimeout, when > 0, is passed as `--fntimeout <n>` (seconds).
	// Sets the function's invocation timeout; default 60s.
	FnTimeout int
	// ExecutorType, when set, picks the function's executor backend
	// ("poolmgr" — default — or "newdeploy").
	ExecutorType string
	// MinScale / MaxScale, when > 0, control newdeploy autoscaler bounds.
	MinScale int
	MaxScale int
	// TargetCPU is the CPU utilization (%) the autoscaler targets.
	TargetCPU int
	// MinCPU / MaxCPU are millicores (CLI: `--mincpu` / `--maxcpu`).
	MinCPU int
	MaxCPU int
	// MinMemory / MaxMemory are MiB (CLI: `--minmemory` / `--maxmemory`).
	MinMemory int
	MaxMemory int
	// ConfigMaps / Secrets attach a configmap or secret by name to the
	// function pods (CLI: `--configmap` / `--secret`, both repeatable).
	ConfigMaps []string
	Secrets    []string
}

// CreateFunction creates a Function via the CLI from either Code or Src. The
// CLI also creates a backing Package with a generated name (`<fn>-<uuid>`).
// Cleanup deletes both: the Function explicitly and any Package whose name
// starts with the function name.
func (ns *TestNamespace) CreateFunction(t *testing.T, ctx context.Context, opts FunctionOptions) {
	t.Helper()
	require.NotEmpty(t, opts.Name, "FunctionOptions.Name")
	sources := 0
	if opts.Code != "" {
		sources++
	}
	if opts.Src != "" {
		sources++
	}
	if opts.Deploy != "" {
		sources++
	}
	if opts.Pkg != "" {
		sources++
	}
	require.Equalf(t, 1, sources,
		"FunctionOptions: exactly one of Code, Src, Deploy, or Pkg must be set (got %+v)", opts)
	if opts.Pkg == "" {
		require.NotEmpty(t, opts.Env, "FunctionOptions.Env required unless Pkg is set")
	}

	args := []string{"fn", "create", "--name", opts.Name}
	switch {
	case opts.Pkg != "":
		args = append(args, "--pkg", opts.Pkg)
		if opts.Entrypoint != "" {
			args = append(args, "--entrypoint", opts.Entrypoint)
		}
	case opts.Code != "":
		args = append(args, "--env", opts.Env, "--code", opts.Code)
	case opts.Deploy != "":
		args = append(args, "--env", opts.Env, "--deploy", opts.Deploy)
		if opts.Entrypoint != "" {
			args = append(args, "--entrypoint", opts.Entrypoint)
		}
	default: // Src
		args = append(args, "--env", opts.Env, "--src", opts.Src)
		if opts.Entrypoint != "" {
			args = append(args, "--entrypoint", opts.Entrypoint)
		}
		if opts.BuildCmd != "" {
			args = append(args, "--buildcmd", opts.BuildCmd)
		}
	}
	if opts.FnTimeout > 0 {
		args = append(args, "--fntimeout", strconv.Itoa(opts.FnTimeout))
	}
	if opts.ExecutorType != "" {
		args = append(args, "--executortype", opts.ExecutorType)
	}
	if opts.MinScale > 0 {
		args = append(args, "--minscale", strconv.Itoa(opts.MinScale))
	}
	if opts.MaxScale > 0 {
		args = append(args, "--maxscale", strconv.Itoa(opts.MaxScale))
	}
	if opts.TargetCPU > 0 {
		args = append(args, "--targetcpu", strconv.Itoa(opts.TargetCPU))
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
	for _, cm := range opts.ConfigMaps {
		args = append(args, "--configmap", cm)
	}
	for _, s := range opts.Secrets {
		args = append(args, "--secret", s)
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("function "+opts.Name, func(c context.Context) error {
		fc := ns.f.fissionClient.CoreV1()
		if err := fc.Functions(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		// The CLI auto-generates a Package named `<fn>-<uuid>`. Find and delete any.
		pkgs, err := fc.Packages(ns.Name).List(c, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, p := range pkgs.Items {
			if strings.HasPrefix(p.Name, opts.Name+"-") {
				if delErr := fc.Packages(ns.Name).Delete(c, p.Name, metav1.DeleteOptions{}); delErr != nil && !apierrors.IsNotFound(delErr) {
					return delErr
				}
			}
		}
		return nil
	})
}

// WaitForFunction polls until the Function CR exists in the test namespace, or
// the timeout elapses. Use this when the CLI returns before the controller has
// processed the request.
func (ns *TestNamespace) WaitForFunction(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(ctx, name, metav1.GetOptions{})
		assert.NoErrorf(c, err, "function %q not visible in namespace %q", name, ns.Name)
	}, 30*time.Second, 500*time.Millisecond)
}

// FunctionLogs returns the combined log output of every specialized pod
// backing this function (selected by `functionName=<fnName>` label), read
// directly via the Kubernetes API. Mirrors `fission function logs --name
// <fn> --detail` for assertion purposes.
//
// We don't go through the CLI here because its `function logs` subcommand
// streams pod logs to os.Stdout, which our in-process CLI helper does not
// capture (it only routes cobra's SetOut/SetErr writers). os.Stdout
// redirection would be unsafe under t.Parallel — pulling logs via kubeClient
// is race-free and matches what the CLI would print.
//
// Pod label notes (verified from Kind 1.34 with skaffold kind-ci):
//   - poolmgr runtime pods: `environmentName=<env>`, `executorType=poolmgr`,
//     and `functionName=<fn>` once specialized.
//   - builder pods (separate deployment): `envName=<env>` (legacy label).
func (ns *TestNamespace) FunctionLogs(t *testing.T, ctx context.Context, fnName string) string {
	t.Helper()
	pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
		LabelSelector: "functionName=" + fnName,
	})
	require.NoErrorf(t, err, "FunctionLogs: list pods (functionName=%s)", fnName)

	fn, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoErrorf(t, err, "FunctionLogs: get function %q", fnName)
	envName := fn.Spec.Environment.Name

	var combined strings.Builder
	for _, p := range pods.Items {
		// In poolmgr pods the function container name equals the env name.
		req := ns.f.kubeClient.CoreV1().Pods(ns.Name).GetLogs(p.Name, &corev1.PodLogOptions{Container: envName})
		stream, err := req.Stream(ctx)
		if err != nil {
			t.Logf("FunctionLogs: stream %s/%s: %v", p.Name, envName, err)
			continue
		}
		b, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if readErr != nil {
			t.Logf("FunctionLogs: read %s/%s: %v", p.Name, envName, readErr)
		}
		combined.Write(b)
	}
	return combined.String()
}

// GetFunction returns the live Function CR by name. Use it to assert on
// fields the controller has populated (e.g. Spec.InvokeStrategy.MinScale,
// Spec.Resources).
func (ns *TestNamespace) GetFunction(t *testing.T, ctx context.Context, fnName string) *fv1.Function {
	t.Helper()
	fn, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetFunction: get function %q", fnName)
	return fn
}

// FunctionPackageName returns the auto-generated Package name backing a
// Function (Spec.Package.PackageRef.Name). Mirrors the bash one-liner:
// `kubectl get functions <fn> -o jsonpath='{.spec.package.packageref.name}'`.
//
// `fission fn update --src` patches the existing Package in place, so this
// name is *stable* across rebuilds — use Package.Status.LastUpdateTimestamp
// (see WaitForPackageRebuiltSince) to detect a rebuild instead.
func (ns *TestNamespace) FunctionPackageName(t *testing.T, ctx context.Context, fnName string) string {
	t.Helper()
	fn, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoErrorf(t, err, "FunctionPackageName: get function %q", fnName)
	require.NotEmptyf(t, fn.Spec.Package.PackageRef.Name,
		"FunctionPackageName: function %q has no package reference yet", fnName)
	return fn.Spec.Package.PackageRef.Name
}
