// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
	"github.com/fission/fission/test/integration/testdata"
)

// writeEchoEnvPy materializes a function that echoes the named environment
// variables as KEY=VALUE lines. Asserting on what the RUNTIME received — not
// on the pod spec — is the point: RFC-0030 is about what reaches the process,
// and a pod-spec assertion would pass even if the kubelet resolved the value
// differently than intended.
func writeEchoEnvPy(t *testing.T, keys ...string) string {
	t.Helper()
	tpl, err := testdata.FS.ReadFile("python/env_vars/echoenv.py.template")
	require.NoError(t, err)
	body := strings.ReplaceAll(string(tpl), "{{ FN_ENV_KEYS }}", strings.Join(keys, ","))
	dst := filepath.Join(t.TempDir(), "echoenv.py")
	require.NoError(t, os.WriteFile(dst, []byte(body), 0o644))
	return dst
}

// TestFunctionEnvVars covers the RFC-0030 phase-1 surface end to end on the
// newdeploy executor: literal env vars, whole-object projection from a Secret
// and a ConfigMap, and key-level references with and without renaming.
//
// It is the E1 assertion narrowed to the executors phase 1 supports — the
// values a function declares are the values it observes.
func TestFunctionEnvVars(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "py-fnenv-" + ns.ID
	fnName := "fn-env-" + ns.ID
	secName := "env-sec-" + ns.ID
	cmName := "env-cm-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})
	ns.CreateSecret(t, ctx, secName, map[string]string{"token": "s3cr3t", "other": "ignored"})
	ns.CreateConfigMap(t, ctx, cmName, map[string]string{"level": "debug"})

	code := writeEchoEnvPy(t, "DATABASE_URL", "token", "API_TOKEN", "level", "LOG_LEVEL")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: code,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 2,
		EnvVars: []string{"DATABASE_URL=postgres://db"},
		// whole object (every data key becomes a variable), one key by its own
		// name, and one key renamed.
		EnvFromSecrets:    []string{secName, secName + "/token:API_TOKEN"},
		EnvFromConfigMaps: []string{cmName + "/level:LOG_LEVEL"},
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("DATABASE_URL=postgres://db"))
	assert.Contains(t, body, "DATABASE_URL=postgres://db", "a literal env var must reach the runtime")
	assert.Contains(t, body, "token=s3cr3t", "a whole-object secret projection must expose each data key")
	assert.Contains(t, body, "API_TOKEN=s3cr3t", "a key-level secret reference must be renamable")
	assert.Contains(t, body, "LOG_LEVEL=debug", "a key-level configmap reference must be renamable")
}

// TestFunctionEnvVarRotation is E4, and the reason the watcher-index and
// RVSum extensions had to ship in phase 1 rather than later: Kubernetes never
// refreshes env in a running container, so rotating a Secret that is
// referenced ONLY through the new env fields must still recycle the pods.
// Without that propagation the function would serve stale credentials
// indefinitely — worse than the gap the feature closes.
func TestFunctionEnvVarRotation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "py-envrot-" + ns.ID
	fnName := "fn-envrot-" + ns.ID
	secName := "rot-sec-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})
	ns.CreateSecret(t, ctx, secName, map[string]string{"token": "before-rotation"})

	code := writeEchoEnvPy(t, "API_TOKEN")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: code,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 2,
		// Referenced ONLY through the env field — the function carries no
		// --secret, so nothing but the RFC-0030 index can notice the rotation.
		EnvFromSecrets: []string{secName + "/token:API_TOKEN"},
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("API_TOKEN=before-rotation"))
	require.Contains(t, body, "API_TOKEN=before-rotation")

	// Rotate the value in place. No function edit, no CLI update — the
	// executor's Secret watcher is the only thing that can propagate this.
	sec, err := f.KubeClient().CoreV1().Secrets(ns.Name).Get(ctx, secName, metav1.GetOptions{})
	require.NoError(t, err)
	sec.Data = map[string][]byte{"token": []byte("after-rotation")}
	_, err = f.KubeClient().CoreV1().Secrets(ns.Name).Update(ctx, sec, metav1.UpdateOptions{})
	require.NoError(t, err)

	body = f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("API_TOKEN=after-rotation"))
	assert.Contains(t, body, "API_TOKEN=after-rotation",
		"rotating a Secret referenced only via env must recycle the function's pods")
}

// TestFunctionEnvVarsPoolmgrRejected pins the phase gate. Poolmgr injection
// rides the specialize payload and needs the capability-gated runtime
// contract, so until that lands a poolmgr function declaring env must be
// REJECTED at admission rather than deployed with a silently empty
// DATABASE_URL — which is the failure this whole feature exists to prevent.
func TestFunctionEnvVarsPoolmgrRejected(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "py-envgate-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	code := writeEchoEnvPy(t, "DATABASE_URL")
	_, err := ns.CLICaptureStdoutWithEnvBestEffort(t, ctx, nil, "fn", "create", "--name", "fn-gate-"+ns.ID,
		"--env", envName, "--code", code,
		"--executortype", "poolmgr", "--env-var", "DATABASE_URL=postgres://db")
	require.Error(t, err, "poolmgr must reject env/envFrom until the specialize-payload phase lands")
	assert.Contains(t, strings.ToLower(err.Error()), "poolmgr",
		"the rejection must name the executor so the user knows what to change")
}

// TestFunctionEnvVarsReservedNameRejected pins the denylist at admission.
// The authoritative control is injection order (platform vars are emitted
// last, so the kubelet's last-wins resolution makes them win), but admission
// is the fast, friendly half — and FISSION_STATE_URL is the load-bearing
// case: redirecting it would exfiltrate the RFC-0023 state token.
func TestFunctionEnvVarsReservedNameRejected(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "py-envresv-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})
	code := writeEchoEnvPy(t, "FISSION_STATE_URL")

	for _, reserved := range []string{"FISSION_STATE_URL", "LD_PRELOAD", "HTTPS_PROXY"} {
		t.Run(reserved, func(t *testing.T) {
			_, err := ns.CLICaptureStdoutWithEnvBestEffort(t, ctx, nil, "fn", "create", "--name", "fn-resv-"+strings.ToLower(reserved)+"-"+ns.ID,
				"--env", envName, "--code", code,
				"--executortype", "newdeploy", "--env-var", reserved+"=http://attacker")
			require.Errorf(t, err, "%s must be rejected as a reserved platform name", reserved)
		})
	}
}
