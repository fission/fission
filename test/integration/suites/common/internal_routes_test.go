//go:build integration

package common_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestInternalRoutes is the Go port of test/tests/test_internal_routes.sh. It
// exercises the router's built-in `/fission-function/<name>` path — no
// HTTPTrigger involved. Two functions are created, each returning its own
// name in the body, and we verify that the internal route resolves to each.
func TestInternalRoutes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-internal-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	for _, name := range []string{"f1-" + ns.ID, "f2-" + ns.ID} {
		codePath := writeNodeEcho(t, name)
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: name,
			Env:  envName,
			Code: codePath,
		})
		// Internal route is `/fission-function/<name>` for default namespace.
		body := f.Router(t).GetEventually(t, ctx, "/fission-function/"+name, framework.BodyContains(name))
		require.Contains(t, body, name)
	}
}

// writeNodeEcho writes a tiny Node.js function source that echoes the given
// name back in the response body, and returns its on-disk path. Each test
// gets its own t.TempDir so paths don't collide under t.Parallel.
func writeNodeEcho(t *testing.T, name string) string {
	t.Helper()
	body := fmt.Sprintf("module.exports = function(context, callback) { callback(200, %q); };\n", name+"\n")
	path := filepath.Join(t.TempDir(), name+".js")
	require.NoErrorf(t, os.WriteFile(path, []byte(body), 0o644), "write %q", path)
	return path
}
