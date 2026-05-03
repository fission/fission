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

// CLI runs a Fission CLI command in-process (no fork/exec) with this
// namespace as the default. Returns combined stdout+stderr and t.Fatals on
// non-zero exit. The same in-process pattern is used by test/e2e/framework/cli.
func (ns *TestNamespace) CLI(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	ns.f.logger.Info("CLI", "ns", ns.Name, "args", args)
	cliMu.RLock()
	defer cliMu.RUnlock()
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
	ns.f.logger.Info("CLICaptureStdout", "ns", ns.Name, "args", args)
	cliMu.Lock()
	defer cliMu.Unlock()

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err, "os.Pipe")
	os.Stdout = w

	stdoutDone := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		stdoutDone <- string(b)
	}()

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

	require.NoErrorf(t, execErr, "fission %s\n%s\n%s", strings.Join(args, " "), buf.String(), captured)
	return captured + buf.String()
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
