// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionTest is the Go port of test_function_test/test_fn_test.sh
// (was bash-disabled per https://github.com/fission/fission/issues/653,
// which describes the invalid-function specialize hang we hit again
// during the first migration attempt).
//
// This test hits the internal route /fission-function/<fn> directly via
// the framework's signed HTTP client — it covers the route itself, NOT
// the CLI command. The CLI command is covered by TestFunctionTestCLI below.
// Only the valid-function case is asserted: the invalid-function variant's
// runtime never returns a SyntaxError body — the specialize step fails and
// the router request hangs until http.Client.Timeout, which makes the test
// unstable regardless of poll budget. The invalid case is the bug fission#653
// was filed against; once that's fixed, restore the second assertion.
func TestFunctionTest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-fntest-" + ns.ID
	validFn := "fnt-valid-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	validCode := framework.WriteTestData(t, "nodejs/fn_test/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: validFn, Env: envName, Code: validCode,
	})
	body := f.Router(t).GetEventually(t, ctx, "/fission-function/"+validFn,
		framework.BodyContains("Hello, Fission"))
	require.True(t, strings.Contains(body, "Hello, Fission"),
		"valid function body missing 'Hello, Fission': %q", body)
}

// TestFunctionTestCLI drives the actual `fission fn test` CLI command
// in-process — the code path that regressed in fission#3588 after the
// router public/internal listener split (GHSA-3g33-6vg6-27m8). The CLI
// must port-forward to the INTERNAL listener (port 8889) and HMAC-sign
// the /fission-function/<ns>/<fn> request, not hit the public listener
// (8888) which returns 404 for that path.
//
// FISSION_INTERNAL_AUTH_SECRET is forwarded from the framework to the CLI
// subprocess env so the CLI's HMAC signing transport can authenticate
// against the internal listener when auth is enabled; when auth is off
// (pass-through mode) the secret is empty and no signing is needed.
func TestFunctionTestCLI(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-fntest-cli-" + ns.ID
	validFn := "fnt-cli-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	validCode := framework.WriteTestData(t, "nodejs/fn_test/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: validFn, Env: envName, Code: validCode,
	})

	// Forward the internal auth secret to the CLI subprocess env so the
	// CLI's HMAC transport can sign the request when auth is enabled.
	env := map[string]string{}
	if secret := string(f.InternalAuthSecret()); secret != "" {
		env["FISSION_INTERNAL_AUTH_SECRET"] = secret
	}

	// `fission fn test` writes the function body to os.Stdout (not cobra's
	// buffer), so use CLICaptureStdoutWithEnv which captures both.
	out := ns.CLICaptureStdoutWithEnv(t, ctx, env, "fn", "test", "--name", validFn, "--method", "GET")
	require.Contains(t, out, "Hello, Fission",
		"fission fn test output missing function body: %q", out)
}

// TestFunctionTestCLIAsync drives the actual `fission fn test --async` CLI
// command in-process. This covers the refactored invokeAsync → buildInternalRequest
// path (RFC-0024): the CLI must port-forward to the INTERNAL listener (port 8889),
// HMAC-sign the request, set X-Fission-Invoke-Mode: async, and print
// "Accepted (202)" + the invocation id. Skips when async invocation is not
// enabled on the router (ASYNC_INVOCATION_ENABLED != true).
func TestFunctionTestCLIAsync(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)

	// Skip if async invocation is not enabled on the router.
	dep, err := f.KubeClient().AppsV1().Deployments(f.FissionNamespace()).Get(ctx, "router", metav1.GetOptions{})
	require.NoError(t, err)
	asyncEnabled := false
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "ASYNC_INVOCATION_ENABLED" && e.Value == "true" {
				asyncEnabled = true
			}
		}
	}
	if !asyncEnabled {
		t.Skip("async invocation is not enabled on the router (ASYNC_INVOCATION_ENABLED != true); skipping")
	}

	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-fntest-async-" + ns.ID
	validFn := "fnt-async-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	validCode := framework.WriteTestData(t, "nodejs/fn_test/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: validFn, Env: envName, Code: validCode,
	})

	// Forward the internal auth secret to the CLI subprocess env so the
	// CLI's HMAC transport can sign the request when auth is enabled.
	env := map[string]string{}
	if secret := string(f.InternalAuthSecret()); secret != "" {
		env["FISSION_INTERNAL_AUTH_SECRET"] = secret
	}

	// `fission fn test --async` prints "Accepted (202)\ninvocationId: <id>"
	// via fmt.Printf (os.Stdout), so use CLICaptureStdoutWithEnv.
	out := ns.CLICaptureStdoutWithEnv(t, ctx, env, "fn", "test", "--name", validFn, "--method", "POST", "--async")
	require.Contains(t, out, "Accepted (202)",
		"fission fn test --async output missing 'Accepted (202)': %q", out)
	require.Contains(t, out, "invocationId:",
		"fission fn test --async output missing 'invocationId:': %q", out)
}
