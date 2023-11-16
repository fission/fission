package cli_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

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
	defer f.Logger().Sync()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := f.Start(ctx)
	require.NoError(t, err)

	err = services.StartServices(ctx, f, mgr)
	if err != nil {
		f.Logger().Error("error starting services", zap.Error(err))
		f.Logger().Sync()
	}
	require.NoError(t, err)

	err = wait.PollUntilContextTimeout(ctx, time.Second*5, time.Second*50, true, func(_ context.Context) (bool, error) {
		if err := f.CheckService("webhook"); err != nil {
			fmt.Println("waiting for webhook service...", err)
			return false, nil
		}
		return true, nil
	})
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

		t.Run("list", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "list")
			require.NoError(t, err)
		})

		t.Run("get", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "get", "--name", "test-env")
			require.NoError(t, err)
		})

		t.Run("pods", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "pods", "--name", "test-env")
			require.NoError(t, err)
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

		t.Run("test/poolmgr", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "test", "--name", testFuncName)
			require.Error(t, err)
		})

		t.Run("test/newdeploy", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "test", "--name", testFuncNd)
			require.Error(t, err)
		})

		t.Run("test/container", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "test", "--name", testFuncCn)
			require.Error(t, err)
		})

		t.Run("list", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "list")
			require.NoError(t, err)
		})

		t.Run("get/poolmgr", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "get", "--name", testFuncName)
			require.NoError(t, err)
		})

		t.Run("getmeta/poolmgr", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "getmeta", "--name", testFuncName)
			require.NoError(t, err)
		})

		t.Run("pods/poolmgr", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "function", "pods", "--name", testFuncName)
			require.NoError(t, err)
		})

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

	t.Run("timetrigger", func(t *testing.T) {

		t.Run("create", func(t *testing.T) {
			// create env and function first
			_, err := cli.ExecCommand(f, ctx, "env", "create", "--name", "test-func-env", "--image", "fission/python-env")
			require.NoError(t, err)
			_, err = cli.ExecCommand(f, ctx, "function", "create", "--name", "test-func", "--code", "./hello.js", "--env", "test-func-env")
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "timetrigger", "create", "--name", "test-tt", "--function", "test-func", "--cron", "@every 1m")
			require.NoError(t, err)

			tt, err := fissionClient.CoreV1().TimeTriggers(metav1.NamespaceDefault).Get(ctx, "test-tt", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, tt)
			require.Equal(t, "test-tt", tt.Name)
			require.Equal(t, "test-func", tt.Spec.FunctionReference.Name)
			require.Equal(t, "@every 1m", tt.Spec.Cron)
		})

		t.Run("list", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "timetrigger", "list")
			require.NoError(t, err)
		})

		t.Run("showschedule", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "timetrigger", "showschedule", "--cron", "@every 1m")
			require.NoError(t, err)
		})

		t.Run("update", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "timetrigger", "update", "--name", "test-tt", "--cron", "@every 2m")
			require.NoError(t, err)

			tt, err := fissionClient.CoreV1().TimeTriggers(metav1.NamespaceDefault).Get(ctx, "test-tt", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, tt)
			require.Equal(t, "test-tt", tt.Name)
			require.Equal(t, "test-func", tt.Spec.FunctionReference.Name)
			require.Equal(t, "@every 2m", tt.Spec.Cron)
		})

		t.Run("delete", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "timetrigger", "delete", "--name", "test-tt")
			require.NoError(t, err)

			_, err = fissionClient.CoreV1().TimeTriggers(metav1.NamespaceDefault).Get(ctx, "test-tt", metav1.GetOptions{})
			require.Error(t, err)

			_, err = cli.ExecCommand(f, ctx, "function", "delete", "--name", "test-func")
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "env", "delete", "--name", "test-func-env")
			require.NoError(t, err)
		})
	})

	t.Run("mqtrigger", func(t *testing.T) {

		t.Run("create", func(t *testing.T) {
			// create env and function first
			_, err := cli.ExecCommand(f, ctx, "env", "create", "--name", "test-func-env", "--image", "fission/python-env")
			require.NoError(t, err)
			_, err = cli.ExecCommand(f, ctx, "function", "create", "--name", "test-func", "--code", "./hello.js", "--env", "test-func-env")
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "mqtrigger", "create", "--name", "test-mqtrigger", "--function", "test-func", "--mqtype", "kafka", "--topic", "test-topic", "--resptopic", "test-resp-topic")
			require.NoError(t, err)

			mqt, err := fissionClient.CoreV1().MessageQueueTriggers(metav1.NamespaceDefault).Get(ctx, "test-mqtrigger", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, mqt)
			require.Equal(t, "test-mqtrigger", mqt.Name)
			require.Equal(t, "test-func", mqt.Spec.FunctionReference.Name)
			require.Equal(t, "test-topic", mqt.Spec.Topic)
			require.Equal(t, "test-resp-topic", mqt.Spec.ResponseTopic)
		})

		t.Run("list", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "mqtrigger", "list")
			require.NoError(t, err)
		})

		t.Run("delete", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "mqtrigger", "delete", "--name", "test-mqtrigger")
			require.NoError(t, err)

			_, err = fissionClient.CoreV1().MessageQueueTriggers(metav1.NamespaceDefault).Get(ctx, "test-mqtrigger", metav1.GetOptions{})
			require.Error(t, err)

			_, err = cli.ExecCommand(f, ctx, "function", "delete", "--name", "test-func")
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "env", "delete", "--name", "test-func-env")
			require.NoError(t, err)
		})

	})

	t.Run("check", func(t *testing.T) {
		_, err := cli.ExecCommand(f, ctx, "check")
		require.NoError(t, err)
	})

	t.Run("version", func(t *testing.T) {
		_, err := cli.ExecCommand(f, ctx, "version")
		require.NoError(t, err)
	})

	t.Run("support-dump", func(t *testing.T) {
		_, err := cli.ExecCommand(f, ctx, "support", "dump")
		require.NoError(t, err)
	})
}
