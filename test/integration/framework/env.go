// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"strconv"
	"testing"
	"time"

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
	// KeepArchive passes `--keeparchive` so the fetcher leaves the deploy
	// archive as a single file instead of extracting it into a directory.
	// Required by the JVM environments: a .jar is itself a zip, so without
	// this the runtime sees /userfunc/deployarchive as a directory and
	// `io.fission.Server.specialize` fails to open it as a JarFile.
	KeepArchive bool
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

// WaitForInternalAuthMaster blocks until the internal-auth master Secret is
// present in this namespace, or the context / a bounded deadline elapses. It
// closes a cluster/dynamic-tenancy cold-window race for tests that assert a
// fetcher-DERIVED agent-identity token (RFC agent-identity, Task 6): in those
// tenancy modes the chart's master-bearing Secret is replicated into a fresh
// function namespace ASYNCHRONOUSLY, so a pool pod created before the copy
// lands sees an empty FISSION_INTERNAL_AUTH_SECRET and its fetcher writes the
// "dev-unauthenticated" placeholder token for that pod's whole life -- and
// poolmgr then pins the function to that placeholder pod, so every retry keeps
// hitting it. Calling this before CreateEnv guarantees every pool pod for the
// environment post-dates the copy and therefore derives a real token.
//
// Best-effort by design, so it can never turn the race into a hard failure:
//   - On a pass-through install (the framework itself holds no master) there is
//     nothing to copy, so it returns immediately.
//   - On timeout it returns WITHOUT failing the test -- the caller's own
//     derived-token assertion still stands, so a genuine derivation bug is not
//     masked, only the benign copy race is removed.
func (ns *TestNamespace) WaitForInternalAuthMaster(t *testing.T, ctx context.Context) {
	t.Helper()
	if len(ns.f.InternalAuthSecret()) == 0 {
		return // pass-through install: no master is ever copied in
	}
	name := fv1.InternalAuthSecretName()
	const (
		timeout = 90 * time.Second
		tick    = 2 * time.Second
	)
	deadline := time.Now().Add(timeout)
	for {
		s, err := ns.f.kubeClient.CoreV1().Secrets(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if err == nil && len(s.Data["secret"]) > 0 {
			return
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			t.Logf("WaitForInternalAuthMaster: master secret %s/%s not observed within %s; "+
				"proceeding (the caller's derived-token assertion still applies)", ns.Name, name, timeout)
			return
		}
		time.Sleep(tick)
	}
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
	if opts.KeepArchive {
		args = append(args, "--keeparchive")
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
	// package build. The buildermgr POSTs to the env's *builder* Service
	// (named `<env>-<resourceVersion>`, selecting builder pods with
	// envName=<env>) on port 8000 (fetcher) and 8001 (builder). If the
	// builder pod is still in ContainerCreating, or kube-proxy hasn't
	// published its endpoint yet, the dial times out
	// (`dial tcp ...:8000: i/o timeout`). Pre-wait for both pod readiness
	// and EndpointSlice readiness so tests don't have to remember.
	//
	// Note: the runtime pool pods are *not* behind the builder Service;
	// they live in a separate Deployment with the `environmentName` label.
	// We don't wait on them here because the build doesn't talk to them.
	if opts.Builder != "" {
		ns.WaitForBuilderReady(t, ctx, opts.Name)
		ns.waitForBuilderEndpointReady(t, ctx, opts.Name)
	}
}
