// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package helm holds chart↔code drift checks: the Helm chart mirrors the
// port/service-name constants in pkg/svcinfo, and nothing but these tests
// ties the two together. They exec `helm template` (skipping when helm is
// not installed — CI runners have it) and compare the rendered manifests
// against the constants.
package helm

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/fission/fission/pkg/svcinfo"
)

// render runs `helm template` on the chart with the given extra args and
// returns the manifest stream split into unstructured docs.
func render(t *testing.T, extraArgs ...string) []map[string]any {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; skipping chart drift check")
	}
	_, filename, _, _ := runtime.Caller(0) //nolint
	chart := filepath.Join(filepath.Dir(filename), "..", "..", "charts", "fission-all")
	args := append([]string{"template", "fission", chart, "--namespace", "fission"}, extraArgs...)
	cmd := exec.CommandContext(t.Context(), helm, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoErrorf(t, err, "helm template failed: %s", stderr.String())

	var docs []map[string]any
	for doc := range strings.SplitSeq(string(out), "\n---") {
		var m map[string]any
		if yaml.Unmarshal([]byte(doc), &m) != nil || m == nil {
			continue
		}
		docs = append(docs, m)
	}
	require.NotEmpty(t, docs)
	return docs
}

// find returns the first doc with the given kind and metadata.name.
func find(docs []map[string]any, kind, name string) map[string]any {
	for _, d := range docs {
		if d["kind"] != kind {
			continue
		}
		if md, ok := d["metadata"].(map[string]any); ok && md["name"] == name {
			return d
		}
	}
	return nil
}

// servicePorts returns the ports slice of a Service doc.
func servicePorts(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	require.NotNil(t, doc)
	spec := doc["spec"].(map[string]any)
	raw := spec["ports"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

// containerArgs returns the first container's args of a Deployment doc.
func containerArgs(t *testing.T, doc map[string]any) []string {
	t.Helper()
	require.NotNil(t, doc)
	containers := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	raw, _ := containers[0].(map[string]any)["args"].([]any)
	out := make([]string, 0, len(raw))
	for _, a := range raw {
		out = append(out, fmt.Sprint(a))
	}
	return out
}

// containerEnv returns the first container's env name→value map.
func containerEnv(t *testing.T, doc map[string]any) map[string]string {
	t.Helper()
	require.NotNil(t, doc)
	containers := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	raw, _ := containers[0].(map[string]any)["env"].([]any)
	out := map[string]string{}
	for _, e := range raw {
		m := e.(map[string]any)
		if v, ok := m["value"].(string); ok {
			out[fmt.Sprint(m["name"])] = v
		}
	}
	return out
}

// argAfter returns the argument following flag in args ("" when absent).
func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestChartPortsMatchSvcinfo is the drift check: every port/URL surface the
// chart renders must equal the pkg/svcinfo constants. If this test fails you
// changed one side without the other — Services, NetworkPolicies, and probes
// come from the chart, while Go code dials the constants.
func TestChartPortsMatchSvcinfo(t *testing.T) {
	docs := render(t, "--set", "mcp.enabled=true", "--set", "mcp.allowInsecure=true")

	t.Run("router deployment args and container ports", func(t *testing.T) {
		router := find(docs, "Deployment", svcinfo.SvcRouter)
		args := containerArgs(t, router)
		assert.Equal(t, fmt.Sprint(svcinfo.PortRouter), argAfter(args, "--routerPort"))
		assert.Equal(t, fmt.Sprint(svcinfo.PortRouterInternal), argAfter(args, "--routerInternalPort"))

		containers := router["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
		ports, _ := containers[0].(map[string]any)["ports"].([]any)
		got := make([]any, 0, len(ports))
		for _, p := range ports {
			got = append(got, p.(map[string]any)["containerPort"])
		}
		assert.Contains(t, got, float64(svcinfo.PortRouter))
		assert.Contains(t, got, float64(svcinfo.PortRouterInternal))
	})

	t.Run("router services", func(t *testing.T) {
		public := servicePorts(t, find(docs, "Service", svcinfo.SvcRouter))
		require.Len(t, public, 1)
		assert.EqualValues(t, svcinfo.PortRouter, public[0]["targetPort"])

		internal := servicePorts(t, find(docs, "Service", svcinfo.SvcRouterInternal))
		require.Len(t, internal, 1)
		assert.EqualValues(t, svcinfo.PortRouterInternal, internal[0]["port"])
		assert.EqualValues(t, svcinfo.PortRouterInternal, internal[0]["targetPort"])
	})

	t.Run("executor and storagesvc services", func(t *testing.T) {
		executor := servicePorts(t, find(docs, "Service", svcinfo.SvcExecutor))
		require.Len(t, executor, 1)
		assert.EqualValues(t, svcinfo.PortExecutor, executor[0]["targetPort"])

		storage := servicePorts(t, find(docs, "Service", svcinfo.SvcStorage))
		require.Len(t, storage, 1)
		assert.EqualValues(t, svcinfo.PortStorageSvc, storage[0]["targetPort"])
	})

	t.Run("mcp deployment arg", func(t *testing.T) {
		args := containerArgs(t, find(docs, "Deployment", "mcp"))
		assert.Equal(t, fmt.Sprint(svcinfo.PortMCP), argAfter(args, "--mcpPort"))
	})

	t.Run("mcp service", func(t *testing.T) {
		mcp := servicePorts(t, find(docs, "Service", svcinfo.SvcMCP))
		require.Len(t, mcp, 1)
		assert.EqualValues(t, svcinfo.PortMCP, mcp[0]["port"])
		assert.EqualValues(t, svcinfo.PortMCP, mcp[0]["targetPort"])
	})

	t.Run("ROUTER_INTERNAL_URL on internal publishers", func(t *testing.T) {
		want := svcinfo.RouterInternalURL("fission")
		for _, name := range []string{"kubewatcher", "timer", "mqtrigger-keda", "mcp"} {
			doc := find(docs, "Deployment", name)
			require.NotNilf(t, doc, "deployment %s must render under the test flags", name)
			assert.Equalf(t, want, containerEnv(t, doc)["ROUTER_INTERNAL_URL"],
				"deployment %s ROUTER_INTERNAL_URL", name)
		}
	})

	t.Run("sibling-service URL args", func(t *testing.T) {
		router := containerArgs(t, find(docs, "Deployment", svcinfo.SvcRouter))
		assert.Equal(t, svcinfo.ExecutorURL("fission"), argAfter(router, "--executorUrl"))

		buildermgr := containerArgs(t, find(docs, "Deployment", "buildermgr"))
		assert.Equal(t, svcinfo.StorageSvcURL("fission"), argAfter(buildermgr, "--storageSvcUrl"))
	})

	t.Run("webhook deployment port", func(t *testing.T) {
		args := containerArgs(t, find(docs, "Deployment", "webhook"))
		assert.Equal(t, fmt.Sprint(svcinfo.PortWebhook), argAfter(args, "--webhookPort"))
	})
}
