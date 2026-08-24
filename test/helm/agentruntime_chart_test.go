// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/fission/fission/pkg/svcinfo"
)

// TestAgentRuntimeChartMaxContinuations checks Task 16's chart wiring: the
// agentruntime Deployment carries AGENT_MAX_CONTINUATIONS from
// agentRuntime.maxContinuations, defaulting to 1000 and following an
// explicit override (including the documented 0 = unlimited sentinel).
func TestAgentRuntimeChartMaxContinuations(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "1000", env["AGENT_MAX_CONTINUATIONS"])
	})

	t.Run("override, including 0 = unlimited", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "agentRuntime.maxContinuations=0")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "0", env["AGENT_MAX_CONTINUATIONS"])
	})
}

// TestAgentRuntimeChartMaxSessionsDefault checks Task #5's chart wiring: the
// agentruntime Deployment carries AGENT_MAX_SESSIONS_DEFAULT from
// agentRuntime.maxSessions, defaulting to 1000 and following an explicit
// override (including the documented 0 = unlimited sentinel).
func TestAgentRuntimeChartMaxSessionsDefault(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "1000", env["AGENT_MAX_SESSIONS_DEFAULT"])
	})

	t.Run("override, including 0 = unlimited", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "agentRuntime.maxSessions=0")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "0", env["AGENT_MAX_SESSIONS_DEFAULT"])
	})
}

// TestAgentRuntimeChartNetworkPolicy checks Task #8: an ingress NetworkPolicy
// for the agentruntime dispatcher renders only when both networkPolicy.enabled
// and agentRuntime.enabled, targets the agentruntime pods, and admits port
// 8894. It is intentionally permissive (no `from` allowlist) so the framework's
// portless integration forward is never dropped — see the template's comment.
func TestAgentRuntimeChartNetworkPolicy(t *testing.T) {
	t.Run("absent when networkPolicy disabled", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		assert.Nil(t, docs.find("NetworkPolicy", "agentruntime"),
			"no agentruntime NetworkPolicy should render when networkPolicy.enabled is false")
	})

	t.Run("renders and admits port 8894 when enabled", func(t *testing.T) {
		docs := render(t,
			"--set", "networkPolicy.enabled=true",
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		np := networkPolicy(t, docs, "agentruntime")

		assert.Equal(t, "agentruntime", np.Spec.PodSelector.MatchLabels["svc"],
			"the policy must target the agentruntime pods")
		require.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
		require.Len(t, np.Spec.Ingress, 1)

		var ports []int32
		for _, p := range np.Spec.Ingress[0].Ports {
			require.NotNil(t, p.Port)
			ports = append(ports, p.Port.IntVal)
		}
		assert.Contains(t, ports, int32(8894), "ingress must admit the agent runtime dispatch port 8894")
		assert.Empty(t, np.Spec.Ingress[0].From, "the policy is deliberately permissive: no from-allowlist")
	})
}

// TestAgentRuntimeChartPoolRBAC guards Task 19's RBAC lockstep: pool
// introspection (GET /registry/pool) needs get/list/watch on
// endpointslices (discovery.k8s.io) + pods, granted through the single
// shared "agentruntime-rules" define — a missing grant here would silently
// degrade every replica's pool panel to a permanent 503 in any cluster
// whose RBAC came from this chart, with nothing else to catch it
// (pkg/agentruntime's own tests use a fake clientset, which enforces no
// RBAC at all). No "services" grant is expected: the informer watches
// EndpointSlices directly, PoolAPI lists Pods, and checkPoolRBAC's
// preflight names only endpointslices and pods.
//
// Static tenancy renders only the namespaced Role; dynamic/cluster tenancy
// additionally renders the cluster-wide ClusterRole (tenant-controller/
// dynamic-cluster-roles.yaml) — both must carry the same grant, since both
// share the one "agentruntime-rules" define.
func TestAgentRuntimeChartPoolRBAC(t *testing.T) {
	wantChecks := []struct{ group, resource string }{
		{"discovery.k8s.io", "endpointslices"},
		{"", "pods"},
	}

	t.Run("Role (static tenancy)", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		found := false
		for _, role := range roles(t, docs) {
			if role.Kind != "Role" || !strings.Contains(role.Name, "agentruntime") {
				continue
			}
			found = true
			for _, chk := range wantChecks {
				verbs := verbsFor(role.Rules, chk.group, chk.resource)
				for _, want := range []string{"get", "list", "watch"} {
					assert.Truef(t, verbs[want], "Role %q must grant %q on %s/%s (have %v)", role.Name, want, chk.group, chk.resource, verbs)
				}
			}
		}
		require.True(t, found, "no agentruntime Role was rendered")
	})

	t.Run("ClusterRole (dynamic tenancy)", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "tenancy.mode=dynamic")
		found := false
		for _, role := range roles(t, docs) {
			if role.Kind != "ClusterRole" || !strings.Contains(role.Name, "agentruntime") {
				continue
			}
			found = true
			for _, chk := range wantChecks {
				verbs := verbsFor(role.Rules, chk.group, chk.resource)
				for _, want := range []string{"get", "list", "watch"} {
					assert.Truef(t, verbs[want], "ClusterRole %q must grant %q on %s/%s (have %v)", role.Name, want, chk.group, chk.resource, verbs)
				}
			}
		}
		require.True(t, found, "no agentruntime ClusterRole was rendered under tenancy.mode=dynamic")
	})
}
