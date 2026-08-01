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

// containerPorts returns the first container's declared containerPort numbers.
func containerPorts(t *testing.T, doc map[string]any) []int {
	t.Helper()
	require.NotNil(t, doc)
	containers := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	raw, _ := containers[0].(map[string]any)["ports"].([]any)
	out := make([]int, 0, len(raw))
	for _, p := range raw {
		out = append(out, int(p.(map[string]any)["containerPort"].(float64)))
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

// countByKindName returns how many docs have the given kind and metadata.name.
func countByKindName(docs []map[string]any, kind, name string) int {
	n := 0
	for _, d := range docs {
		if d["kind"] == kind {
			if md, ok := d["metadata"].(map[string]any); ok && md["name"] == name {
				n++
			}
		}
	}
	return n
}

// TestWebhookEnvReaderRBACScopesByTenancy pins the RFC-0023 webhook
// environment-read RBAC to the same tenancy posture as every other Fission-CRD
// read (review feedback on #3593): a namespaced Role per Fission namespace in
// static mode (a cluster-wide grant would be over-privileged), and a
// cluster-wide ClusterRole added only in dynamic/cluster mode.
func TestWebhookEnvReaderRBACScopesByTenancy(t *testing.T) {
	const roleName = "fission-webhook-env-reader" // render() uses release name "fission"

	t.Run("static: namespaced Roles, no ClusterRole", func(t *testing.T) {
		docs := render(t, "--set", "additionalFissionNamespaces={fission-fn}")
		assert.GreaterOrEqual(t, countByKindName(docs, "Role", roleName), 2,
			"a Role in defaultNamespace + each additionalFissionNamespaces")
		assert.Zero(t, countByKindName(docs, "ClusterRole", roleName),
			"static tenancy must not grant cluster-wide environment read")
	})

	t.Run("dynamic: adds the ClusterRole", func(t *testing.T) {
		docs := render(t, "--set", "tenancy.mode=dynamic")
		assert.Equal(t, 1, countByKindName(docs, "ClusterRole", roleName),
			"dynamic tenancy onboards namespaces at runtime, so the read is cluster-wide")
	})
}

// npAllowsFromSvc reports whether any ingress rule's `from` allowlist admits the
// given svc label via a podSelector.
func npAllowsFromSvc(doc map[string]any, svcLabel string) bool {
	spec, _ := doc["spec"].(map[string]any)
	ingress, _ := spec["ingress"].([]any)
	for _, r := range ingress {
		rule, _ := r.(map[string]any)
		froms, _ := rule["from"].([]any)
		for _, f := range froms {
			sel, _ := f.(map[string]any)["podSelector"].(map[string]any)
			ml, _ := sel["matchLabels"].(map[string]any)
			if ml["svc"] == svcLabel {
				return true
			}
		}
	}
	return false
}

// TestWorkflowChart is the drift check for the RFC-0022 workflow head: port
// constants against svcinfo, statestore env wiring, and — the known
// silent-drop bite — membership in BOTH NetworkPolicy allowlists (router
// internal listener + statestore).
func TestWorkflowChart(t *testing.T) {
	docs := render(t,
		"--set", "workflows.enabled=true",
		"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
		"--set", "networkPolicy.enabled=true",
		"--set", "serviceMonitor.enabled=true")

	t.Run("workflow deployment arg and env", func(t *testing.T) {
		deploy := find(docs, "Deployment", svcinfo.SvcWorkflow)
		args := containerArgs(t, deploy)
		assert.Equal(t, fmt.Sprint(svcinfo.PortWorkflow), argAfter(args, "--workflowPort"))

		env := containerEnv(t, deploy)
		assert.Equal(t, "client", env["STATESTORE_DRIVER"])
		assert.Contains(t, env["STATESTORE_DSN"], svcinfo.SvcStatestore)
		assert.Equal(t, svcinfo.RouterInternalURL("fission"), env["ROUTER_INTERNAL_URL"])
	})

	t.Run("workflow service", func(t *testing.T) {
		svc := servicePorts(t, find(docs, "Service", svcinfo.SvcWorkflow))
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortWorkflow, svc[0]["port"])
		assert.EqualValues(t, svcinfo.PortWorkflow, svc[0]["targetPort"])
	})

	t.Run("networkpolicy allowlists admit svc:workflow", func(t *testing.T) {
		routerNP := find(docs, "NetworkPolicy", "router-allow-ingress")
		require.NotNil(t, routerNP)
		assert.True(t, npAllowsFromSvc(routerNP, svcinfo.SvcWorkflow),
			"the engine invokes on the internal listener; a missing row is a silent i/o timeout in CI")

		ssNP := find(docs, "NetworkPolicy", "statestore")
		require.NotNil(t, ssNP)
		assert.True(t, npAllowsFromSvc(ssNP, svcinfo.SvcWorkflow),
			"the engine reads/writes its log on the statestore")
	})

	t.Run("metrics are scrapable", func(t *testing.T) {
		// A ServiceMonitor without the pod exposing 8080 (or vice versa) is
		// silently-unscraped metrics — pin both halves.
		sm := find(docs, "ServiceMonitor", "workflow-monitor")
		require.NotNil(t, sm, "workflow ServiceMonitor must render when serviceMonitor.enabled")

		deploy := find(docs, "Deployment", svcinfo.SvcWorkflow)
		ports := containerPorts(t, deploy)
		assert.Contains(t, ports, 8080, "metrics containerPort")
	})

	t.Run("disabled by default", func(t *testing.T) {
		docs := render(t)
		assert.Nil(t, find(docs, "Deployment", svcinfo.SvcWorkflow))
	})
}

// TestAsyncInvocationChart checks the RFC-0024 chart wiring: the router gains the
// async env and the internal-listener NetworkPolicy admits svc: router (the
// dispatcher's cross-replica delivery) only when asyncInvocation.enabled, and
// neither renders by default.
func TestAsyncInvocationChart(t *testing.T) {
	t.Run("enabled: router env + svc:router NetworkPolicy row", func(t *testing.T) {
		docs := render(t,
			"--set", "asyncInvocation.enabled=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "networkPolicy.enabled=true")

		env := containerEnv(t, find(docs, "Deployment", svcinfo.SvcRouter))
		assert.Equal(t, "true", env["ASYNC_INVOCATION_ENABLED"])
		assert.Equal(t, "client", env["STATESTORE_DRIVER"])
		assert.Contains(t, env["STATESTORE_DSN"], svcinfo.SvcStatestore)
		assert.Equal(t, svcinfo.RouterInternalURL("fission"), env["ROUTER_INTERNAL_URL"])

		np := find(docs, "NetworkPolicy", "router-allow-ingress")
		require.NotNil(t, np, "router NetworkPolicy must render with networkPolicy.enabled")
		assert.True(t, npAllowsFromSvc(np, svcinfo.SvcRouter),
			"async delivery is cross-replica; the internal-listener allowlist must admit svc: router")
	})

	t.Run("disabled by default: no async env, no svc:router row", func(t *testing.T) {
		docs := render(t, "--set", "networkPolicy.enabled=true")
		env := containerEnv(t, find(docs, "Deployment", svcinfo.SvcRouter))
		assert.NotContains(t, env, "ASYNC_INVOCATION_ENABLED")
		np := find(docs, "NetworkPolicy", "router-allow-ingress")
		require.NotNil(t, np)
		assert.False(t, npAllowsFromSvc(np, svcinfo.SvcRouter),
			"svc: router must not be in the allowlist unless async is enabled")
	})
}

// TestStatestoreChartPorts is the drift check for the embedded statestore head:
// its --statestorePort arg and Service port must equal svcinfo.PortStatestore
// (RFC-0021). It renders with the store enabled, since it is off by default.
func TestStatestoreChartPorts(t *testing.T) {
	docs := render(t, "--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")

	t.Run("statestore deployment arg", func(t *testing.T) {
		args := containerArgs(t, find(docs, "Deployment", svcinfo.SvcStatestore))
		assert.Equal(t, fmt.Sprint(svcinfo.PortStatestore), argAfter(args, "--statestorePort"))
	})

	t.Run("statestore service", func(t *testing.T) {
		svc := servicePorts(t, find(docs, "Service", svcinfo.SvcStatestore))
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortStatestore, svc[0]["port"])
		assert.EqualValues(t, svcinfo.PortStatestore, svc[0]["targetPort"])
	})
}

// TestStateSvcChart is the drift check for the RFC-0023 statesvc head: port
// constants against svcinfo, statestore client-driver env wiring, and
// membership in the statestore NetworkPolicy allowlist (the silent-drop bite).
func TestStateSvcChart(t *testing.T) {
	docs := render(t,
		"--set", "functionState.enabled=true",
		"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
		"--set", "networkPolicy.enabled=true")

	t.Run("statesvc deployment arg and env", func(t *testing.T) {
		deploy := find(docs, "Deployment", svcinfo.SvcStateSvc)
		args := containerArgs(t, deploy)
		assert.Equal(t, fmt.Sprint(svcinfo.PortStateSvc), argAfter(args, "--stateApiPort"))

		env := containerEnv(t, deploy)
		assert.Equal(t, "client", env["STATESTORE_DRIVER"])
		assert.Contains(t, env["STATESTORE_DSN"], svcinfo.SvcStatestore)
	})

	t.Run("statesvc service", func(t *testing.T) {
		svc := servicePorts(t, find(docs, "Service", svcinfo.SvcStateSvc))
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortStateSvc, svc[0]["port"])
		assert.EqualValues(t, svcinfo.PortStateSvc, svc[0]["targetPort"])
	})

	t.Run("statestore networkpolicy admits svc:statesvc", func(t *testing.T) {
		ssNP := find(docs, "NetworkPolicy", "statestore")
		require.NotNil(t, ssNP)
		assert.True(t, npAllowsFromSvc(ssNP, svcinfo.SvcStateSvc),
			"statesvc reads/writes keyspaces on the statestore; a missing row is a silent i/o timeout in CI")
	})

	t.Run("statesvc networkpolicy renders", func(t *testing.T) {
		require.NotNil(t, find(docs, "NetworkPolicy", svcinfo.SvcStateSvc),
			"function pods reach statesvc across namespaces; the policy must render with the head")
	})

	t.Run("metrics are scrapable", func(t *testing.T) {
		// A ServiceMonitor without the pod exposing 8080 (or vice versa) is
		// silently-unscraped metrics — statesvc emits fission_statestore_*
		// through the OTel→Prometheus bridge, so pin both halves.
		mdocs := render(t,
			"--set", "functionState.enabled=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "serviceMonitor.enabled=true")
		require.NotNil(t, find(mdocs, "ServiceMonitor", "statesvc-monitor"),
			"statesvc ServiceMonitor must render when serviceMonitor.enabled")
		deploy := find(mdocs, "Deployment", svcinfo.SvcStateSvc)
		assert.Contains(t, containerPorts(t, deploy), 8080, "metrics containerPort")
	})

	t.Run("renders with coverage enabled (CI profile)", func(t *testing.T) {
		// The kind-ci profile sets coverage.enabled=true; a stray coverage
		// volumeMount without its own volumeMounts key lands under ports and
		// fails the server-side apply (containerPort=0 + mountPath). Render the
		// exact CI shape and assert no port carries a mountPath.
		docs := render(t,
			"--set", "functionState.enabled=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "coverage.enabled=true")
		deploy := find(docs, "Deployment", svcinfo.SvcStateSvc)
		require.NotNil(t, deploy)
		containers := deploy["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
		ports, _ := containers[0].(map[string]any)["ports"].([]any)
		for _, p := range ports {
			_, hasMount := p.(map[string]any)["mountPath"]
			assert.False(t, hasMount, "a port carries a mountPath — coverage volumeMount leaked into ports")
		}
	})

	t.Run("disabled by default", func(t *testing.T) {
		docs := render(t)
		assert.Nil(t, find(docs, "Deployment", svcinfo.SvcStateSvc))
	})
}

// TestInternalAuthExistingSecret pins RFC-0029's `internalAuth.existingSecret`
// contract, whose failure mode is silent.
//
// Every consumer must resolve ONE name. The secretKeyRef is mounted optional so
// rotation can drop a key without forcing an empty render — which also means a
// pod whose reference points at a Secret that does not exist starts happily
// with the env var absent, and then 401s on every archive fetch and builder
// upload with nothing naming the Secret as the cause.
//
// The Go side resolves the same name from FISSION_INTERNAL_AUTH_SECRET_NAME
// (fv1.InternalAuthSecretName), so the chart must set it to whatever it wired
// into the secretKeyRefs.
func TestInternalAuthExistingSecret(t *testing.T) {
	const defaultName = "fission-internal-auth"

	t.Run("default: the chart renders the master and points at it", func(t *testing.T) {
		docs := render(t)
		assert.Positive(t, countByKindName(docs, "Secret", defaultName),
			"with no existingSecret the chart must generate the master itself")
		names := internalAuthEnvNames(docs)
		require.NotEmpty(t, names,
			"no internal-auth secretKeyRef was rendered; the loop below would assert nothing")
		for _, name := range names {
			assert.Equal(t, defaultName, name)
		}
	})

	t.Run("existingSecret: the chart renders no master and points at theirs", func(t *testing.T) {
		docs := render(t, "--set", "internalAuth.existingSecret=my-master")
		assert.Zero(t, countByKindName(docs, "Secret", defaultName),
			"the chart must not generate a master when the operator supplies one")
		names := internalAuthEnvNames(docs)
		require.NotEmpty(t, names, "no internal-auth secretKeyRef was rendered")
		for _, name := range names {
			assert.Equal(t, "my-master", name,
				"every consumer must read the operator's Secret, not the chart's default")
		}
	})

	// The env var is what the Go side reads. If it disagrees with the
	// secretKeyRefs above, the two halves resolve different Secrets.
	t.Run("the name env var matches the secretKeyRefs", func(t *testing.T) {
		for _, tc := range []struct{ arg, want string }{
			{"", defaultName},
			{"my-master", "my-master"},
		} {
			var docs []map[string]any
			if tc.arg == "" {
				docs = render(t)
			} else {
				docs = render(t, "--set", "internalAuth.existingSecret="+tc.arg)
			}
			vals := envValues(docs, "FISSION_INTERNAL_AUTH_SECRET_NAME")
			require.NotEmptyf(t, vals, "FISSION_INTERNAL_AUTH_SECRET_NAME must be set (existingSecret=%q)", tc.arg)
			for _, v := range vals {
				assert.Equal(t, tc.want, v)
			}
		}
	})
}

// internalAuthEnvNames collects the Secret names every FISSION_INTERNAL_AUTH_SECRET
// secretKeyRef points at, across every rendered pod template.
func internalAuthEnvNames(docs []map[string]any) []string {
	var out []string
	forEachContainerEnv(docs, func(e map[string]any) {
		name, _ := e["name"].(string)
		if name != "FISSION_INTERNAL_AUTH_SECRET" {
			return
		}
		vf, _ := e["valueFrom"].(map[string]any)
		skr, _ := vf["secretKeyRef"].(map[string]any)
		if n, ok := skr["name"].(string); ok {
			out = append(out, n)
		}
	})
	return out
}

// envValues collects the literal values of a named env var across every
// rendered pod template.
func envValues(docs []map[string]any, envName string) []string {
	var out []string
	forEachContainerEnv(docs, func(e map[string]any) {
		if name, _ := e["name"].(string); name == envName {
			if v, ok := e["value"].(string); ok {
				out = append(out, v)
			}
		}
	})
	return out
}

// forEachContainerEnv walks every env entry of every container in every
// rendered pod template.
func forEachContainerEnv(docs []map[string]any, fn func(map[string]any)) {
	for _, doc := range docs {
		spec, _ := doc["spec"].(map[string]any)
		tmpl, ok := spec["template"].(map[string]any)
		if !ok {
			continue
		}
		podSpec, _ := tmpl["spec"].(map[string]any)
		for _, key := range []string{"containers", "initContainers"} {
			cs, _ := podSpec[key].([]any)
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				envs, _ := cm["env"].([]any)
				for _, e := range envs {
					if em, ok := e.(map[string]any); ok {
						fn(em)
					}
				}
			}
		}
	}
}

// TestInternalAuthAutoGenerate pins RFC-0029 §10's generation path, including
// the tenancy rule that decides where the master may land.
func TestInternalAuthAutoGenerate(t *testing.T) {
	const master = "fission-internal-auth"

	countMasterSecrets := func(docs []map[string]any) int {
		n := 0
		for _, d := range docs {
			kind, _ := d["kind"].(string)
			meta, _ := d["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			if kind == "Secret" && name == master {
				n++
			}
		}
		return n
	}

	t.Run("off by default: the template still renders the master", func(t *testing.T) {
		docs := render(t)
		assert.Positive(t, countMasterSecrets(docs),
			"autoGenerate defaults off, so existing installs must be untouched")
		assert.Zero(t, countByKindName(docs, "Role", "fission-preupgrade-authgen"),
			"no generation means no read grant")
	})

	t.Run("on: the template stops rendering and the hook is wired", func(t *testing.T) {
		docs := render(t, "--set", "internalAuth.autoGenerate=true")
		assert.Zero(t, countMasterSecrets(docs),
			"generation moves in-cluster, so the template must render no master — "+
				"the retention hook is what stops Helm pruning the live one")
		vals := envValues(docs, "GENERATE_AUTH_SECRET")
		require.NotEmpty(t, vals, "the hook must be told which Secret to provision")
		for _, v := range vals {
			assert.Equal(t, master, v)
		}
	})

	// The rule the whole design turns on: under dynamic/cluster tenancy the
	// master must NOT reach tenant namespaces — they get controller-owned
	// derived keys, and a master there would defeat the isolation end state.
	t.Run("static tenancy fans out; dynamic does not", func(t *testing.T) {
		static := render(t,
			"--set", "internalAuth.autoGenerate=true",
			"--set", "additionalFissionNamespaces={fission-fn}")
		staticNS := envValues(static, "GENERATE_AUTH_SECRET_NAMESPACES")
		require.NotEmpty(t, staticNS)
		assert.Contains(t, staticNS[0], "fission-fn",
			"static tenancy replicates the master: kubelet cannot resolve a cross-namespace secretKeyRef")

		dynamic := render(t,
			"--set", "internalAuth.autoGenerate=true",
			"--set", "tenancy.mode=dynamic",
			"--set", "additionalFissionNamespaces={fission-fn}")
		dynNS := envValues(dynamic, "GENERATE_AUTH_SECRET_NAMESPACES")
		require.NotEmpty(t, dynNS)
		assert.NotContains(t, dynNS[0], "fission-fn",
			"under dynamic tenancy the master must stay in the release namespace: "+
				"tenant namespaces get controller-owned derived keys and must never hold the master")
	})

	// The read grant is the one concession this design makes; it must not widen
	// beyond the single object that needs it.
	t.Run("the generation grant is fenced to the master alone", func(t *testing.T) {
		docs := render(t, "--set", "internalAuth.autoGenerate=true")
		var checked int
		for _, d := range docs {
			kind, _ := d["kind"].(string)
			meta, _ := d["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			if kind != "Role" || name != "fission-preupgrade-authgen" {
				continue
			}
			checked++
			rules, _ := d["rules"].([]any)
			for _, r := range rules {
				rule, _ := r.(map[string]any)
				names, _ := rule["resourceNames"].([]any)
				require.Len(t, names, 1, "the grant must name exactly one Secret")
				assert.Equal(t, master, names[0])
				verbs, _ := rule["verbs"].([]any)
				for _, v := range verbs {
					assert.NotEqual(t, "list", v, "list would let this identity enumerate every Secret")
					assert.NotEqual(t, "delete", v)
				}
			}
		}
		require.Positive(t, checked, "no generation Role was rendered; the guard is inert")
	})
}
