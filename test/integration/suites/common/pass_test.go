//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestPass is the Go port of test/tests/test_pass.sh. The bash version is a
// pure smoke test — it only verifies that env vars are set and the `fission`
// CLI binary exists. Our Go suite implicitly verifies both on every other
// test (every helper drives the in-process CLI), but we keep this as a
// fast-failing canary for Phase 1 debug: if the framework can't even reach
// the cluster, fail in 30s instead of taking down a 5-minute canary test.
func TestPass(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	f := framework.Connect(t)
	require.NotEmpty(t, f.RestConfig().Host, "framework rest config has a host")
	require.NotEmpty(t, f.Router(t).BaseURL(), "router base URL is set")

	// Probe the API server with a no-op CLI call. Catches mis-wired CLI or
	// a control plane that's up but unreachable from the test process.
	ns := f.NewTestNamespace(t)
	out := ns.CLI(t, ctx, "env", "list")
	t.Logf("fission env list: %q", out)
}
