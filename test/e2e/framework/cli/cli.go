package cli

import (
	"bytes"
	"context"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/cmd/fission-cli/app"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/test/e2e/framework"
)

func ExecCommand(f *framework.Framework, ctx context.Context, args ...string) (string, error) {
	f.Logger().Info("Executing command", zap.Strings("args", args))
	cmd := app.App(cmd.ClientOptions{
		RestConfig: f.RestConfig(),
		Namespace:  metav1.NamespaceDefault,
	})
	cmd.SilenceErrors = true // use our own error message printer
	cmd.SetArgs(args)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.ExecuteContext(ctx)
	return buf.String(), err
}
