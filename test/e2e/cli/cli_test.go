package cli_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	f := framework.NewFramework()
	ctx, cancel := context.WithCancel(context.Background())
	err := f.Start(ctx)
	require.NoError(t, err)
	defer func() {
		cancel()
		mgr.Wait()
		err = f.Stop()
		require.NoError(t, err)
	}()

	err = services.StartServices(ctx, f, mgr)
	require.NoError(t, err)

	err = wait.PollUntilContextTimeout(ctx, time.Second*5, time.Second*50, true, func(_ context.Context) (bool, error) {
		if err := f.CheckService("webhook"); err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)

	fissionClient, err := f.ClientGen().GetFissionClient()
	require.NoError(t, err)

	t.Run("environment", func(t *testing.T) {
		defaultEnvVersion := 3
		t.Run("create", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "create", "--name", "test-env", "--image", "fission/python-env")
			require.NoError(t, err)

			env, err := fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Get(ctx, "test-env", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, env)
			require.Equal(t, "test-env", env.Name)
			require.Equal(t, "fission/python-env", env.Spec.Runtime.Image)
			require.Equal(t, defaultEnvVersion, env.Spec.Version)
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

			_, err = cli.ExecCommand(f, ctx, "httptrigger", "create", "--name", "test-httptrigger", "--function", "test-func", "--url", "/hello", "--createingress")
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
			require.Equal(t, "POST", tt.Spec.Method)
			require.Equal(t, "/", tt.Spec.Subpath)
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
			_, err := cli.ExecCommand(f, ctx, "timetrigger", "update", "--name", "test-tt", "--cron", "@every 2m", "--method", "GET", "--subpath", "/api/v1/fetch")
			require.NoError(t, err)

			tt, err := fissionClient.CoreV1().TimeTriggers(metav1.NamespaceDefault).Get(ctx, "test-tt", metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, tt)
			require.Equal(t, "test-tt", tt.Name)
			require.Equal(t, "test-func", tt.Spec.FunctionReference.Name)
			require.Equal(t, "@every 2m", tt.Spec.Cron)
			require.Equal(t, "GET", tt.Spec.Method)
			require.Equal(t, "/api/v1/fetch", tt.Spec.Subpath)
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

	t.Run("package", func(t *testing.T) {
		testPkgName := "test-pkg"
		envName := "test-env"
		envName2 := "test-env-2"

		t.Run("create", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "create", "--name", envName, "--image", "fission/python-env")
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "pkg", "create", "--name", testPkgName, "--code", "./hello.js", "--env", envName)
			require.NoError(t, err)

			pkg, err := fissionClient.CoreV1().Packages(metav1.NamespaceDefault).Get(ctx, testPkgName, metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, pkg)
			require.Equal(t, testPkgName, pkg.Name)
			require.Equal(t, envName, pkg.Spec.Environment.Name)
		})

		t.Run("list", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "pkg", "list")
			require.NoError(t, err)
		})

		t.Run("info", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "pkg", "info", "--name", testPkgName)
			require.NoError(t, err)
		})

		t.Run("getsrc", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "pkg", "getsrc", "--name", testPkgName)
			require.NoError(t, err)
		})

		t.Run("getdeploy", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "pkg", "getdeploy", "--name", testPkgName)
			require.NoError(t, err)
		})

		t.Run("rebuild", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "pkg", "rebuild", "--name", testPkgName)
			require.Error(t, err)
		})

		t.Run("update", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "env", "create", "--name", envName2, "--image", "fission/python-env:v2")
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "pkg", "update", "--name", testPkgName, "--code", "./hello.js", "--env", envName2)
			require.NoError(t, err)

			pkg, err := fissionClient.CoreV1().Packages(metav1.NamespaceDefault).Get(ctx, testPkgName, metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, pkg)
			require.Equal(t, testPkgName, pkg.Name)
			require.Equal(t, envName2, pkg.Spec.Environment.Name)
		})

		t.Run("delete", func(t *testing.T) {
			_, err := cli.ExecCommand(f, ctx, "pkg", "delete", "--name", testPkgName)
			require.NoError(t, err)

			_, err = fissionClient.CoreV1().Packages(metav1.NamespaceDefault).Get(ctx, testPkgName, metav1.GetOptions{})
			require.Error(t, err)

			_, err = cli.ExecCommand(f, ctx, "env", "delete", "--name", envName)
			require.NoError(t, err)

			_, err = cli.ExecCommand(f, ctx, "env", "delete", "--name", envName2)
			require.NoError(t, err)
		})
	})

	t.Run("check", func(t *testing.T) {
		_, err := cli.ExecCommand(f, ctx, "check")
		require.NoError(t, err)
	})

	t.Run("check --namespace", func(t *testing.T) {
		_, err := cli.ExecCommand(f, ctx, "check", "--namespace", "default")
		require.NoError(t, err)
	})

	t.Run("version", func(t *testing.T) {
		_, err := cli.ExecCommand(f, ctx, "version")
		require.NoError(t, err)
	})

	t.Run("support-dump", func(t *testing.T) {
		_, err := cli.ExecCommand(f, ctx, "support", "dump")
		require.NoError(t, err)

		err = os.RemoveAll("fission-dump")
		require.NoError(t, err)
	})

	t.Run("archive", func(t *testing.T) {
		out, err := cli.ExecCommand(f, ctx, "archive", "upload", "--name", "hello.js")
		require.NoError(t, err)
		require.Contains(t, out, "File successfully uploaded with ID")
		id := out[len("File successfully uploaded with ID: "):]
		// split string by / and get the last element
		id = strings.Split(id, "/")[len(strings.Split(id, "/"))-1]
		id = strings.Trim(id, "\n")

		_, err = cli.ExecCommand(f, ctx, "archive", "list")
		require.NoError(t, err)

		_, err = cli.ExecCommand(f, ctx, "archive", "get-url", "--id", id)
		require.NoError(t, err)

		_, err = cli.ExecCommand(f, ctx, "archive", "download", "--id", id)
		require.NoError(t, err)

		_, err = cli.ExecCommand(f, ctx, "archive", "delete", "--id", id)
		require.NoError(t, err)
	})

	t.Run("spec", func(t *testing.T) {
		// check if specs directory exists and delete it
		if info, err := os.Stat("specs"); err == nil && info.IsDir() {
			err = os.RemoveAll("specs")
			require.NoError(t, err)
		}

		_, err := cli.ExecCommand(f, ctx, "spec", "init")
		require.NoError(t, err)

		// create resources
		_, err = cli.ExecCommand(f, ctx, "env", "create", "--name", "test-func-env", "--image", "fission/python-env", "--spec")
		require.NoError(t, err)
		_, err = cli.ExecCommand(f, ctx, "function", "create", "--name", "test-func", "--code", "./hello.js", "--env", "test-func-env", "--spec")
		require.NoError(t, err)
		_, err = cli.ExecCommand(f, ctx, "httptrigger", "create", "--name", "test-httptrigger", "--function", "test-func", "--url", "/hello", "--spec", "--createingress")
		require.NoError(t, err)
		_, err = cli.ExecCommand(f, ctx, "timetrigger", "create", "--name", "test-tt", "--function", "test-func", "--cron", "@every 1m", "--spec")
		require.NoError(t, err)
		_, err = cli.ExecCommand(f, ctx, "mqtrigger", "create", "--name", "test-mqtrigger", "--function", "test-func", "--mqtype", "kafka", "--topic", "test-topic", "--resptopic", "test-resp-topic", "--spec")
		require.NoError(t, err)

		_, err = cli.ExecCommand(f, ctx, "spec", "validate")
		require.NoError(t, err)

		_, err = cli.ExecCommand(f, ctx, "spec", "apply")
		require.NoError(t, err)
		fn, err := fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, "test-func", metav1.GetOptions{})
		require.NoError(t, err)
		require.NotNil(t, fn)
		require.Equal(t, "test-func", fn.Name)

		_, err = cli.ExecCommand(f, ctx, "spec", "list")
		require.NoError(t, err)

		_, err = cli.ExecCommand(f, ctx, "spec", "destroy")
		require.NoError(t, err)
		_, err = fissionClient.CoreV1().Functions(metav1.NamespaceDefault).Get(ctx, "test-func", metav1.GetOptions{})
		require.Error(t, err)

		// cleanup specs directory
		err = os.RemoveAll("specs")
		require.NoError(t, err)
	})
}
