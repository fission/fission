package cli_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/test/e2e/framework"
	"github.com/fission/fission/test/e2e/framework/cli"
	"github.com/fission/fission/test/e2e/framework/services"
)

func TestFissionCLI(t *testing.T) {

	mgr := manager.New()
	defer mgr.Wait()

	f := framework.NewFramework()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := f.Start(ctx)
	require.NoError(t, err)

	err = services.StartServices(ctx, f, mgr)
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

	t.Run("function", func(t *testing.T) {

		envName := "test-func-env"
		testFuncName := "hello"
		testFuncNd := "hello-nd"
		testFuncCn := "hello-cn"

		t.Run("create/poolmgr", func(t *testing.T) {

			_, err = cli.ExecCommand(f, ctx, "env", "create", "--name", envName, "--image", "fission/python-env")
			require.NoError(t, err)

			_, err := cli.ExecCommand(f, ctx, "function", "create", "--name", testFuncName, "--code", "./hello.js", "--env", envName)
			require.NoError(t, err)

			testFunc, err := fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, testFuncName, metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, testFunc)
			require.Equal(t, testFuncName, testFunc.Name)
			require.Equal(t, envName, testFunc.Spec.Environment.Name)
			require.Equal(t, v1.ExecutorTypePoolmgr, testFunc.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)
		})

		t.Run("create/newdeploy", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "create", "--name", testFuncNd, "--code", "./hello.js", "--env", envName, "--executortype", "newdeploy")
			require.NoError(t, err)

			testFunc, err := fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, testFuncNd, metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, testFunc)
			require.Equal(t, testFuncNd, testFunc.Name)
			require.Equal(t, envName, testFunc.Spec.Environment.Name)
			require.Equal(t, v1.ExecutorTypeNewdeploy, testFunc.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)
		})

		t.Run("create/container", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "run-container", "--name", testFuncCn, "--image", "gcr.io/google-samples/node-hello:1.0", "--port", "8080")
			require.NoError(t, err)

			testFunc, err := fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, testFuncCn, metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, testFunc)
			require.Equal(t, testFuncCn, testFunc.Name)
			require.Equal(t, v1.ExecutorTypeContainer, testFunc.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)
		})

		t.Run("update/poolmgr", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "update", "--name", testFuncName, "--labels", "env=test")
			require.NoError(t, err)

			testFunc, err := fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, testFuncName, metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, testFunc)
			require.Equal(t, testFuncName, testFunc.Name)
			require.NotNil(t, testFunc.Labels)
			require.Equal(t, "test", testFunc.Labels["env"])
			require.Equal(t, v1.ExecutorTypePoolmgr, testFunc.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)
		})

		// t.Run("test/poolmgr", func(t *testing.T) {
		// 	_, err := cli.ExecCommand(f, ctx, "function", "test", "--name", testFuncName)
		// 	require.NoError(t, err)
		// })

		// t.Run("test/newdeploy", func(t *testing.T) {
		// 	_, err := cli.ExecCommand(f, ctx, "function", "test", "--name", testFuncNd)
		// 	require.NoError(t, err)
		// })

		// t.Run("test/container", func(t *testing.T) {
		// 	_, err := cli.ExecCommand(f, ctx, "function", "test", "--name", testFuncCn)
		// 	require.NoError(t, err)
		// })

		t.Run("delete/newdeploy", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "delete", "--name", testFuncNd)
			require.NoError(t, err)

			_, err = fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, testFuncNd, metav1.GetOptions{})
			require.Error(t, err)
		})

		t.Run("delete/container", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "delete", "--name", testFuncCn)
			require.NoError(t, err)

			_, err = fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, testFuncCn, metav1.GetOptions{})
			require.Error(t, err)
		})

		t.Run("delete/poolmgr", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "delete", "--name", testFuncName)
			require.NoError(t, err)

			_, err = fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, testFuncName, metav1.GetOptions{})
			require.Error(t, err)
			_, err = cli.ExecCommand(f, ctx, "env", "delete", "--name", envName)
			require.NoError(t, err)
		})

	})

	t.Run("httptrigger", func(t *testing.T) {

		t.Run("create", func(t *testing.T) {
			// create env and function first
			_, err := cli.ExecCommand(f, ctx, "env", "create", "--name", "test-func-env", "--image", "fission/python-env")
			require.NoError(t, err)
			_, err = cli.ExecCommand(f, ctx, "function", "create", "--name", "test-func", "--code", "./hello.js", "--env", "test-func-env")
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "httptrigger", "create", "--name", "test-httptrigger", "--function", "test-func", "--url", "/hello")
			require.NoError(t, err)

			ht, err := fissionClient.CoreV1().HTTPTriggers(metav1.NamespaceDefault).Get(ctx, "test-httptrigger", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, ht)
			require.Equal(t, "test-httptrigger", ht.Name)
			require.Equal(t, "test-func", ht.Spec.FunctionReference.Name)
			require.Equal(t, "/hello", ht.Spec.RelativeURL)
		})

		t.Run("update", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "httptrigger", "update", "--name", "test-httptrigger", "--url", "/hello2")
			require.NoError(t, err)

			ht, err := fissionClient.CoreV1().HTTPTriggers(metav1.NamespaceDefault).Get(ctx, "test-httptrigger", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, ht)
			require.Equal(t, "test-httptrigger", ht.Name)
			require.Equal(t, "test-func", ht.Spec.FunctionReference.Name)
			require.Equal(t, "/hello2", ht.Spec.RelativeURL)
		})

		t.Run("delete", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "httptrigger", "delete", "--name", "test-httptrigger")
			require.NoError(t, err)

			_, err = fissionClient.CoreV1().HTTPTriggers(metav1.NamespaceDefault).Get(ctx, "test-httptrigger", metav1.GetOptions{})
			require.Error(t, err)

			_, err = cli.ExecCommand(f, ctx, "function", "delete", "--name", "test-func")
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "env", "delete", "--name", "test-func-env")
			require.NoError(t, err)
		})

	})

}
