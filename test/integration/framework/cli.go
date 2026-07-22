// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/cmd/fission-cli/app"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

// cliMu guards process-global CLI state (os.Stdout, os.Environ()) that
// CLI variants below mutate. Regular CLI calls take the read lock so they
// run in parallel; CLIWithEnv and CLICaptureStdout take the write lock to
// serialize against any in-flight CLI calls while the global state is
// changed. The cost is that env-var- or stdout-mutating tests block until
// outstanding parallel CLI calls finish.
var cliMu sync.RWMutex

// withNamespaceFlag makes a non-default test namespace (NewTestNamespaceIn)
// take effect for the in-process CLI by passing --namespace explicitly.
//
// The CLI resolves a create/get/delete's target namespace from a *process-
// global* client (cmd.SetClientset is sync.Once-guarded), so the first CLI call
// in the test binary freezes it — and since every NewTestNamespace test uses
// `default`, it freezes to `default`. A later app.App(ClientOptions{Namespace:
// <other>}) then rebuilds a correct per-call client but its SetClientset is a
// no-op, so GetResourceNamespace's c.Client().Namespace fallback still returns
// `default`. Passing --namespace takes GetResourceNamespace's flag-first branch,
// which reads the actual flag and is immune to the frozen global.
//
// Default-namespace callers are returned unchanged (the frozen global is already
// correct for them), and an explicit --namespace/-n already in args wins.
func withNamespaceFlag(args []string, namespace string) []string {
	if namespace == metav1.NamespaceDefault {
		return args
	}
	for _, a := range args {
		if a == "--namespace" || a == "-n" ||
			strings.HasPrefix(a, "--namespace=") || strings.HasPrefix(a, "-n=") {
			return args
		}
	}
	return append(append([]string(nil), args...), "--namespace", namespace)
}

// CLI runs a Fission CLI command in-process (no fork/exec) with this
// namespace as the default. Returns combined stdout+stderr and t.Fatals on
// non-zero exit. The same in-process pattern is used by test/e2e/framework/cli.
func (ns *TestNamespace) CLI(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	ns.f.logger.Info("CLI", "ns", ns.Name, "args", args)
	cliMu.RLock()
	defer cliMu.RUnlock()
	args = withNamespaceFlag(args, ns.Name)
	c := app.App(cmd.ClientOptions{
		RestConfig: ns.f.restConfig,
		Namespace:  ns.Name,
	})
	c.SilenceErrors = true
	c.SilenceUsage = true
	c.SetArgs(args)
	buf := new(bytes.Buffer)
	c.SetOut(buf)
	c.SetErr(buf)
	err := c.ExecuteContext(ctx)
	require.NoErrorf(t, err, "fission %s\n%s", strings.Join(args, " "), buf.String())
	return buf.String()
}

// CLIExpectError runs an in-process CLI command expected to fail. Returns
// combined stdout+stderr + the CLI error. Use for negative tests asserting
// webhook/CEL rejection (e.g. provisionedConcurrency on newdeploy). Fatals
// if the command unexpectedly succeeds; caller asserts the error message.
func (ns *TestNamespace) CLIExpectError(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	ns.f.logger.Info("CLIExpectError", "ns", ns.Name, "args", args)
	cliMu.RLock()
	defer cliMu.RUnlock()
	args = withNamespaceFlag(args, ns.Name)
	c := app.App(cmd.ClientOptions{
		RestConfig: ns.f.restConfig,
		Namespace:  ns.Name,
	})
	c.SilenceErrors = true
	c.SilenceUsage = true
	c.SetArgs(args)
	buf := new(bytes.Buffer)
	c.SetOut(buf)
	c.SetErr(buf)
	err := c.ExecuteContext(ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	return buf.String(), err
}

// CLIWithEnv runs an in-process CLI command with extra environment
// variables set for the duration of the call. Used by tests that exercise
// CLI flags resolved from the process environment (e.g.
// FISSION_DEFAULT_NAMESPACE). Holds cliMu.Lock() so other in-flight CLI
// calls don't race on the env state.
//
// On return, env vars are restored to their prior values.
func (ns *TestNamespace) CLIWithEnv(t *testing.T, ctx context.Context, env map[string]string, args ...string) string {
	t.Helper()
	ns.f.logger.Info("CLIWithEnv", "ns", ns.Name, "env", env, "args", args)
	cliMu.Lock()
	defer cliMu.Unlock()

	restore := setEnvOverrides(env)
	defer restore()

	c := app.App(cmd.ClientOptions{
		RestConfig: ns.f.restConfig,
		Namespace:  ns.Name,
	})
	c.SilenceErrors = true
	c.SilenceUsage = true
	c.SetArgs(args)
	buf := new(bytes.Buffer)
	c.SetOut(buf)
	c.SetErr(buf)
	err := c.ExecuteContext(ctx)
	require.NoErrorf(t, err, "fission %s\n%s", strings.Join(args, " "), buf.String())
	return buf.String()
}

// CLICaptureStdout runs an in-process CLI command and captures everything
// written to os.Stdout in addition to the cobra Out/Err buffer. Used by
// CLI subcommands (e.g. `fission archive list`, `fission archive get-url`)
// that print their results via fmt.Println instead of cobra's writers.
//
// Holds cliMu.Lock() so concurrent CLI calls don't have their stdout
// captured into our buffer. Returns the combined captured-stdout +
// cobra-buffer string.
func (ns *TestNamespace) CLICaptureStdout(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	out, err := ns.cliCaptureStdoutBoth(t, ctx, args...)
	require.NoErrorf(t, err, "fission %s\n%s", strings.Join(args, " "), out)
	return out
}

// CLICaptureStdoutBestEffort is the cleanup-friendly variant of
// CLICaptureStdout: returns the captured output and any error from the
// CLI rather than calling t.Fatal. Use this in t.Cleanup blocks where
// the operation may legitimately fail (e.g. deleting a resource the
// test body already deleted).
func (ns *TestNamespace) CLICaptureStdoutBestEffort(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	return ns.cliCaptureStdoutBoth(t, ctx, args...)
}

// CLICaptureStdoutWithEnv is the env-aware variant of CLICaptureStdout:
// it sets extra env vars for the duration of the call (restored on return)
// AND captures os.Stdout in addition to the cobra Out/Err buffer. Used by
// tests that drive CLI subcommands printing via fmt.Println/os.Stdout
// while also needing env-resolved flags (e.g. `fission fn test` reading
// FISSION_INTERNAL_AUTH_SECRET). Holds cliMu.Lock() so concurrent CLI
// calls don't race on the global stdout or env state.
func (ns *TestNamespace) CLICaptureStdoutWithEnv(t *testing.T, ctx context.Context, env map[string]string, args ...string) string {
	t.Helper()
	out, err := ns.cliCaptureStdoutBothWithEnv(t, ctx, env, args...)
	require.NoErrorf(t, err, "fission %s\n%s", strings.Join(args, " "), out)
	return out
}

// CLICaptureStdoutWithEnvBestEffort is the error-returning variant of
// CLICaptureStdoutWithEnv, for calls that are EXPECTED to fail (e.g. a state
// write past its quota) where the test asserts on the returned error.
func (ns *TestNamespace) CLICaptureStdoutWithEnvBestEffort(t *testing.T, ctx context.Context, env map[string]string, args ...string) (string, error) {
	t.Helper()
	return ns.cliCaptureStdoutBothWithEnv(t, ctx, env, args...)
}

func (ns *TestNamespace) cliCaptureStdoutBoth(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	return ns.cliCaptureStdoutBothWithEnv(t, ctx, nil, args...)
}

func (ns *TestNamespace) cliCaptureStdoutBothWithEnv(t *testing.T, ctx context.Context, env map[string]string, args ...string) (string, error) {
	t.Helper()
	ns.f.logger.Info("CLICaptureStdout", "ns", ns.Name, "env", env, "args", args)
	cliMu.Lock()
	defer cliMu.Unlock()

	restore := setEnvOverrides(env)
	defer restore()

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err, "os.Pipe")
	os.Stdout = w

	stdoutDone := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		stdoutDone <- string(b)
	}()

	args = withNamespaceFlag(args, ns.Name)
	c := app.App(cmd.ClientOptions{
		RestConfig: ns.f.restConfig,
		Namespace:  ns.Name,
	})
	c.SilenceErrors = true
	c.SilenceUsage = true
	c.SetArgs(args)
	buf := new(bytes.Buffer)
	c.SetOut(buf)
	c.SetErr(buf)
	execErr := c.ExecuteContext(ctx)

	_ = w.Close()
	os.Stdout = origStdout
	captured := <-stdoutDone

	return captured + buf.String(), execErr
}

// setEnvOverrides applies the given env vars and returns a restore
// function to call (typically via defer). Empty values are honored.
func setEnvOverrides(env map[string]string) (restore func()) {
	type prev struct {
		key string
		val string
		set bool
	}
	saved := make([]prev, 0, len(env))
	for k, v := range env {
		old, was := os.LookupEnv(k)
		saved = append(saved, prev{key: k, val: old, set: was})
		_ = os.Setenv(k, v)
	}
	return func() {
		for _, p := range saved {
			if p.set {
				_ = os.Setenv(p.key, p.val)
			} else {
				_ = os.Unsetenv(p.key)
			}
		}
	}
}
