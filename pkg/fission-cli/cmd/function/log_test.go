// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func logFunction() *fv1.Function {
	return &fv1.Function{
		Name: "hello", Namespace: "default", UID: "uid-1",
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "nodejs", Namespace: "default"},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypePoolmgr},
			},
		},
	}
}

func logFunctionPod(name, resourceVersion, version string) *corev1.Pod {
	pod := &corev1.Pod{
		Name: name, Namespace: "default", ResourceVersion: resourceVersion,
		Labels: map[string]string{
			fv1.FUNCTION_UID:          "uid-1",
			fv1.ENVIRONMENT_NAME:      "nodejs",
			fv1.ENVIRONMENT_NAMESPACE: "default",
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "hello"}}},
	}
	if version != "" {
		pod.Labels[fv1.FUNCTION_VERSION] = version
	}
	return pod
}

func setLogClients(t *testing.T, fissionObjs []runtime.Object, pods ...runtime.Object) {
	t.Helper()
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(fissionObjs...),
		KubernetesClient: k8sfake.NewClientset(pods...),
		Namespace:        "default",
	})
}

func logInput(dbType string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.SetString(flagkey.FnName, "hello")
	in.SetString(flagkey.Namespace, "default")
	in.SetString(flagkey.FnLogDBType, dbType)
	return in
}

// TestLogAliasVersionMutuallyExclusive guards the --alias/--version mutual
// exclusion for `fn logs`: it must be checked (and error) before any log
// database is contacted.
func TestLogAliasVersionMutuallyExclusive(t *testing.T) {
	fn := logFunction()
	setLogClients(t, []runtime.Object{fn})

	in := logInput("kubernetes")
	in.SetString(flagkey.FnTestAlias, "prod")
	in.SetString(flagkey.FnTestVersion, "hello-v1")

	err := Log(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestLogAliasPreflight guards the --alias preflight for `fn logs`: a
// missing/mismatched alias must error clearly before any pods are read.
func TestLogAliasPreflight(t *testing.T) {
	t.Run("missing alias", func(t *testing.T) {
		fn := logFunction()
		setLogClients(t, []runtime.Object{fn})

		in := logInput("kubernetes")
		in.SetString(flagkey.FnTestAlias, "prod")

		err := Log(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `alias "prod" not found for function "hello"`)
	})

	t.Run("alias targets a different function", func(t *testing.T) {
		fn := logFunction()
		alias := &fv1.FunctionAlias{
			Name: "prod", Namespace: "default",
			Spec: fv1.FunctionAliasSpec{FunctionName: "other-fn", Version: "other-v1"},
		}
		setLogClients(t, []runtime.Object{fn, alias})

		in := logInput("kubernetes")
		in.SetString(flagkey.FnTestAlias, "prod")

		err := Log(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `targets function "other-fn", not "hello"`)
	})
}

// TestLogVersionFilterAppliesToKubernetesSelector is the end-to-end wiring
// check for the kubernetes dbtype: --version must both preflight-check the
// FunctionVersion and narrow pod selection to it. Two pods exist, an older
// one carrying the requested version and a newer one carrying a different
// version; without the filter selectFunctionPods would pick the newer pod
// (see logdb.TestSelectFunctionPodsVersionFilter) -- reading the wrong
// version's logs. A version-scoped, successful (non-error) read here shows
// the filter reached the pod selector.
func TestLogVersionFilterAppliesToKubernetesSelector(t *testing.T) {
	fn := logFunction()
	version := &fv1.FunctionVersion{
		Name: "hello-v1", Namespace: "default",
		Spec: fv1.FunctionVersionSpec{FunctionName: "hello"},
	}
	older := logFunctionPod("pod-v1", "1", "hello-v1")
	newer := logFunctionPod("pod-v2", "9", "hello-v2")
	setLogClients(t, []runtime.Object{fn, version}, older, newer)

	in := logInput("kubernetes")
	in.SetString(flagkey.FnTestVersion, "hello-v1")
	in.SetInt(flagkey.FnLogCount, 10)

	require.NoError(t, Log(in))
}

// TestLogVersionFilterWarnsForNonKubernetesDbType guards the warn-and-ignore
// gating for --alias/--version on a dbtype that cannot apply it (mirrors the
// existing --request-id/--trace-id/--level gating for non-loki dbtypes, just
// inverted): the loki driver has no notion of a FunctionVersion pod label, so
// the CLI must warn rather than silently drop or error on the flag.
func TestLogVersionFilterWarnsForNonKubernetesDbType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":[]}}`))
	}))
	defer srv.Close()
	t.Setenv("LOKI_URL", srv.URL)

	fn := logFunction()
	version := &fv1.FunctionVersion{
		Name: "hello-v1", Namespace: "default",
		Spec: fv1.FunctionVersionSpec{FunctionName: "hello"},
	}
	setLogClients(t, []runtime.Object{fn, version})

	in := logInput("loki")
	in.SetString(flagkey.FnTestVersion, "hello-v1")
	in.SetInt(flagkey.FnLogCount, 10)

	out := captureStdout(t, func() error { return Log(in) })
	assert.Contains(t, out, "Warning")
	assert.Contains(t, out, "--alias/--version filters are only applied by the kubernetes dbtype")
}
