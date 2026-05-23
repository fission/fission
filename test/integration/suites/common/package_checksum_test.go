//go:build integration

package common_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestPackageChecksum is the Go port of test/tests/test_package_checksum.sh.
// It exercises the four URL-based package-creation modes the CLI supports:
//
//   - `fn create --code <url>`              → CLI computes and stores SHA256.
//   - `fn update --code <new-url>`          → checksum updates with new content.
//   - `fn create --code <url> --deploychecksum <hex>` → user-provided checksum.
//   - `fn create --code <url> --insecure`   → skip checksum, store empty.
//
// Plus the equivalent `pkg create` paths.
//
// The test serves the package payloads from an in-process httptest server
// rather than reaching out to github.com/fission/examples — the CLI runs
// in-process (see ns.CLI), so 127.0.0.1:<random> is reachable, and the
// buildermgr never re-fetches the URL (the CLI submits the literal payload
// to the Package CR). This keeps the test deterministic and independent of
// external network conditions.
func TestPackageChecksum(t *testing.T) {
	t.Parallel()

	const (
		helloJS    = "module.exports = function(context, callback) { callback(200, \"hello!\\n\"); };\n"
		callbackJS = "module.exports = function(context, callback) { callback(200, \"callback!\\n\"); };\n"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/hello.js", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(helloJS))
	})
	mux.HandleFunc("/hello-callback.js", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(callbackJS))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	url1 := srv.URL + "/hello.js"
	url2 := srv.URL + "/hello-callback.js"
	sum1 := sha256Hex(helloJS)
	sum2 := sha256Hex(callbackJS)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pkgsum-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image, GracePeriod: 5})

	t.Run("fn_create_with_url", func(t *testing.T) {
		fnName := "fn1-" + ns.ID
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: url1})
		pkgName := ns.FunctionPackageName(t, ctx, fnName)
		require.Equal(t, sum1, ns.PackageDeployChecksum(t, ctx, pkgName),
			"package deploy checksum should match SHA256 of fetched URL content")
		// Wait for the buildermgr to finish building (download + builder run)
		// before hitting the router. The router-poll budget only covers route
		// reconcile + executor specialization; build can overflow it on slow CI.
		ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))

		// Update fn → URL2; checksum should change.
		ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--env", envName, "--code", url2)
		pkgName2 := ns.FunctionPackageName(t, ctx, fnName)
		require.Equal(t, sum2, ns.PackageDeployChecksum(t, ctx, pkgName2),
			"package deploy checksum should reflect updated URL content")
		ns.WaitForPackageBuildSucceeded(t, ctx, pkgName2)
		f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("callback"))
	})

	t.Run("pkg_create_modes", func(t *testing.T) {
		pkg1 := "pkg1-" + ns.ID
		pkg2 := "pkg2-" + ns.ID
		pkg3 := "pkg3-" + ns.ID

		// CLI computes checksum from URL.
		ns.CreatePackage(t, ctx, framework.PackageOptions{Name: pkg1, Env: envName, Deploy: url1})
		require.Equal(t, sum1, ns.PackageDeployChecksum(t, ctx, pkg1))
		ns.CLI(t, ctx, "pkg", "update", "--name", pkg1, "--env", envName, "--deploy", url2)
		require.Equal(t, sum2, ns.PackageDeployChecksum(t, ctx, pkg1),
			"pkg update --deploy <url> should refresh checksum")

		// User-provided checksum is accepted (and stored).
		ns.CreatePackage(t, ctx, framework.PackageOptions{
			Name: pkg2, Env: envName, Deploy: url1, DeployChecksum: sum1,
		})
		require.Equal(t, sum1, ns.PackageDeployChecksum(t, ctx, pkg2))

		// --insecure stores empty checksum.
		ns.CreatePackage(t, ctx, framework.PackageOptions{
			Name: pkg3, Env: envName, Deploy: url1, Insecure: true,
		})
		require.Empty(t, ns.PackageDeployChecksum(t, ctx, pkg3),
			"pkg create --insecure should leave the checksum empty")
	})
}

// sha256Hex returns the lowercase hex SHA256 of s. Used by TestPackageChecksum
// to compute the expected checksum that the CLI should arrive at independently
// after downloading the payload from the in-process httptest server.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
