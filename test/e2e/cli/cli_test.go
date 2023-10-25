package cli_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/e2e/framework"
	"github.com/fission/fission/test/e2e/framework/cli"
	"github.com/fission/fission/test/e2e/framework/services"
)

func TestFissionCLI(t *testing.T) {
	f := framework.NewFramework()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := f.Start(ctx)
	require.NoError(t, err)

	err = services.StartServices(ctx, f)
	require.NoError(t, err)

	fissionClient, err := f.ClientGen().GetFissionClient()
	require.NoError(t, err)

	t.Run("environment", func(t *testing.T) {
		t.Run("create", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "create", "--name", "test-env", "--image", "fission/python-env")
			require.NoError(t, err)

			env, err := fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Get(ctx, "test-env", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, env)
			require.Equal(t, "test-env", env.Name)
			require.Equal(t, "fission/python-env", env.Spec.Runtime.Image)
		})

		t.Run("update", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "update", "--name", "test-env", "--image", "fission/python-env:v2")
			require.NoError(t, err)

			env, err := fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Get(ctx, "test-env", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, env)
			require.Equal(t, "test-env", env.Name)
			require.Equal(t, "fission/python-env:v2", env.Spec.Runtime.Image)
		})

		t.Run("delete", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "delete", "--name", "test-env")
			require.NoError(t, err)

			_, err = fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Get(ctx, "test-env", metav1.GetOptions{})
			require.Error(t, err)
		})
	})
}
