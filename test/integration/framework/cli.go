//go:build integration

package framework

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/cmd/fission-cli/app"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

// CLI runs a Fission CLI command in-process (no fork/exec) with this
// namespace as the default. Returns combined stdout+stderr and t.Fatals on
// non-zero exit. The same in-process pattern is used by test/e2e/framework/cli.
func (ns *TestNamespace) CLI(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	ns.f.logger.Info("CLI", "ns", ns.Name, "args", args)
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
