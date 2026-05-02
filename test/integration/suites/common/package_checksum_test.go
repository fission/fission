//go:build integration

package common_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
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
func TestPackageChecksum(t *testing.T) {
	t.Parallel()

	const url1 = "https://raw.githubusercontent.com/fission/examples/main/nodejs/hello.js"
	const url2 = "https://raw.githubusercontent.com/fission/examples/main/nodejs/hello-callback.js"

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	sum1 := fetchSHA256(t, ctx, url1)
	sum2 := fetchSHA256(t, ctx, url2)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pkgsum-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image, GracePeriod: 5})

	t.Run("fn_create_with_url", func(t *testing.T) {
		fnName := "fn1-" + ns.ID
		ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: url1})
		pkgName := ns.FunctionPackageName(t, ctx, fnName)
		require.Equal(t, sum1, ns.PackageDeployChecksum(t, ctx, pkgName),
			"package deploy checksum should match SHA256 of fetched URL content")
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
		f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))

		// Update fn → URL2; checksum should change.
		ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--env", envName, "--code", url2)
		pkgName2 := ns.FunctionPackageName(t, ctx, fnName)
		require.Equal(t, sum2, ns.PackageDeployChecksum(t, ctx, pkgName2),
			"package deploy checksum should reflect updated URL content")
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

// fetchSHA256 downloads url and returns the lowercase hex SHA256 of its body.
// Used by TestPackageChecksum to compute the expected checksum that the CLI
// should arrive at independently.
func fetchSHA256(t *testing.T, ctx context.Context, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	require.NoErrorf(t, err, "fetchSHA256 GET %q", url)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "fetchSHA256 %q non-2xx", url)
	h := sha256.New()
	_, err = io.Copy(h, resp.Body)
	require.NoErrorf(t, err, "fetchSHA256 read %q", url)
	return hex.EncodeToString(h.Sum(nil))
}
