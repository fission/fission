// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// RFC-0025: live integration coverage for `fission fn test --alias/--version`
// (pkg/fission-cli/cmd/function/test.go's resolveTestRefSuffix), reusing the
// aliasFixture skeleton alias_routing_test.go's TestAlias* battery already
// established in this package. `fn test` hits the router INTERNAL listener
// directly by suffixed name ("/fission-function/<fn>:<alias|version>", see
// utils.UrlForFunctionRef) -- unlike every TestAlias* test above it needs no
// HTTPTrigger/createRoute at all, just the same FISSION_INTERNAL_AUTH_SECRET
// forwarding TestFunctionTestCLI (function_test_test.go) uses so the CLI's
// HMAC transport can sign against the internal listener.
package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestFnTestAliasVersion proves `fission fn test --alias`/`--version` route
// to the resolved FunctionAlias/pinned FunctionVersion instead of the live
// function, and that repointing the alias is immediately visible through
// `--alias` without touching `--version` at all -- a rollback-verification
// story: `fn test --alias prod` before/after a repoint is exactly the smoke
// test an operator would run to confirm a rollback landed. Also covers the
// three error paths resolveTestRefSuffix rejects before ever sending a
// request: --alias and --version together, a missing alias, a missing
// version.
func TestFnTestAliasVersion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "fntest", "prod")

	const v1Marker = "fntest-alias-marker-v1"
	const v2Marker = "fntest-alias-marker-v2"
	af.publishTwoVersions(t, ctx, image,
		framework.FunctionOptions{Code: writeNodeReturning(t, "v1", v1Marker+"\n")},
		"--code", writeNodeReturning(t, "v2", v2Marker+"\n"))

	af.createAlias(t, ctx, af.V1Name)

	// Forward the internal auth secret into the CLI's process env -- see
	// TestFunctionTestCLI (function_test_test.go) for why this is needed
	// and CLICaptureStdoutWithEnv (not CLI) is used: `fn test` writes the
	// function body straight to os.Stdout, not cobra's buffer.
	env := map[string]string{}
	if secret := string(f.InternalAuthSecret()); secret != "" {
		env["FISSION_INTERNAL_AUTH_SECRET"] = secret
	}

	// --- --alias resolves to the alias's currently-resolved version (v1) ---
	// Warm the internal `:<alias>` route first (same per-function/per-suffix
	// mux-registration race TestFunctionTestCLI guards against) via a plain
	// GET, distinct from the `fn test` CLI call under test itself.
	aliasPath := "/fission-function/" + af.FnName + ":" + af.AliasName
	f.Router(t).GetEventually(t, ctx, aliasPath, framework.BodyContains(v1Marker))

	out := af.ns.CLICaptureStdoutWithEnv(t, ctx, env, "fn", "test", "--name", af.FnName, "--method", "GET", "--alias", af.AliasName)
	assert.Containsf(t, out, v1Marker, "fn test --alias %s must serve the alias-resolved version (v1): %q", af.AliasName, out)

	// --- --version pins a specific FunctionVersion (v2), independent of
	// what the alias currently resolves to ---
	versionPath := "/fission-function/" + af.FnName + ":" + af.V2Name
	f.Router(t).GetEventually(t, ctx, versionPath, framework.BodyContains(v2Marker))

	out = af.ns.CLICaptureStdoutWithEnv(t, ctx, env, "fn", "test", "--name", af.FnName, "--method", "GET", "--version", af.V2Name)
	assert.Containsf(t, out, v2Marker, "fn test --version %s must serve the pinned version (v2): %q", af.V2Name, out)

	// --- repoint the alias to v2: `fn test --alias prod` now returns the
	// v2 marker with no other change -- the rollback-verification story ---
	af.ns.CLICaptureStdout(t, ctx, "alias", "update", "--name", af.AliasName, "--version", af.V2Name)
	f.Router(t).GetEventually(t, ctx, aliasPath, framework.BodyContains(v2Marker))

	out = af.ns.CLICaptureStdoutWithEnv(t, ctx, env, "fn", "test", "--name", af.FnName, "--method", "GET", "--alias", af.AliasName)
	assert.Containsf(t, out, v2Marker, "fn test --alias %s must reflect the repoint to v2: %q", af.AliasName, out)

	// --- error paths (resolveTestRefSuffix's preflight, no request sent) ---
	t.Run("alias_and_version_mutually_exclusive", func(t *testing.T) {
		_, err := af.ns.CLIExpectError(t, ctx, "fn", "test", "--name", af.FnName, "--method", "GET",
			"--alias", af.AliasName, "--version", af.V1Name)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("missing_alias", func(t *testing.T) {
		missingAlias := "no-such-alias-" + af.ns.ID
		_, err := af.ns.CLIExpectError(t, ctx, "fn", "test", "--name", af.FnName, "--method", "GET",
			"--alias", missingAlias)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("missing_version", func(t *testing.T) {
		missingVersion := af.FnName + "-v99"
		_, err := af.ns.CLIExpectError(t, ctx, "fn", "test", "--name", af.FnName, "--method", "GET",
			"--version", missingVersion)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}
